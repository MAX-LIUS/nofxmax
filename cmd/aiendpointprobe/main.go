// Command aiendpointprobe verifies that every configured AI endpoint (primary +
// fallbacks) for a provider is actually reachable and usable.
//
// It deliberately reuses the SAME code path production uses:
//   - mcp.NewAIClientByProvider(provider) to get the real provider client, so
//     the wire format (Anthropic /messages vs OpenAI /chat/completions), auth
//     header and body shape are identical to a live trader call.
//   - the key-inheritance rule from Client.switchToNextEndpoint: a fallback with
//     an empty api_key inherits the key currently in use, which in the per-call
//     failover flow is always the PRIMARY key (CallWithMessages calls
//     restoreToPrimary before trying primary, and after every fallback attempt).
//
// The database is opened READ-ONLY (mode=ro) and never migrated. API keys are
// decrypted in memory and never printed; only lengths and a masked prefix.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"nofx/crypto"
	"nofx/mcp"
	_ "nofx/mcp/provider" // register provider factories
	"nofx/store"
)

// probeTarget is one endpoint to test, with the key already resolved.
type probeTarget struct {
	Provider string
	Role     string // "primary" or "fallback[i]"
	Name     string
	BaseURL  string
	Model    string
	APIKey   string
	KeySrc   string // where the key came from: "own" or "inherited:primary"
	Priority int
}

func mask(k string) string {
	if k == "" {
		return "<empty>"
	}
	if len(k) <= 12 {
		return fmt.Sprintf("len=%d <too-short-to-mask>", len(k))
	}
	return fmt.Sprintf("len=%d %s...%s", len(k), k[:4], k[len(k)-4:])
}

// loadTargets reads one ai_models row and expands it into primary + fallbacks,
// applying the same key-inheritance semantics as switchToNextEndpoint.
func loadTargets(db *sql.DB, cs *crypto.CryptoService, provider string) ([]probeTarget, error) {
	var encKey, apiURL, model, fbJSON string
	var enabled int
	row := db.QueryRow(`SELECT api_key, custom_api_url, custom_model_name,
		COALESCE(fallback_endpoints,''), enabled
		FROM ai_models WHERE provider=? LIMIT 1`, provider)
	if err := row.Scan(&encKey, &apiURL, &model, &fbJSON, &enabled); err != nil {
		return nil, fmt.Errorf("query %s row: %w", provider, err)
	}
	if enabled != 1 {
		fmt.Printf("  note: provider %s has enabled=0 (probing anyway)\n", provider)
	}

	primaryKey := encKey
	if cs.IsEncryptedStorageValue(encKey) {
		dec, err := cs.DecryptFromStorage(encKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s primary key: %w", provider, err)
		}
		primaryKey = dec
	}

	targets := []probeTarget{{
		Provider: provider,
		Role:     "primary",
		Name:     "(primary)",
		BaseURL:  apiURL,
		Model:    model,
		APIKey:   primaryKey,
		KeySrc:   "own",
		Priority: -1,
	}}

	if strings.TrimSpace(fbJSON) == "" {
		return targets, nil
	}
	var fbs []store.FallbackEndpoint
	if err := json.Unmarshal([]byte(fbJSON), &fbs); err != nil {
		return nil, fmt.Errorf("parse %s fallback_endpoints: %w", provider, err)
	}
	// Match Client.SetFallbackEndpoints: sort by Priority ascending, stable, so
	// the probe reports endpoints in the exact order production will try them.
	sort.SliceStable(fbs, func(i, j int) bool { return fbs[i].Priority < fbs[j].Priority })
	for i, fb := range fbs {
		key, src := fb.APIKey, "own"
		if key == "" {
			// Mirrors switchToNextEndpoint: an empty fallback key leaves the
			// client's current key in place, which is the primary's key.
			key, src = primaryKey, "inherited:primary"
		} else if cs.IsEncryptedStorageValue(key) {
			dec, err := cs.DecryptFromStorage(key)
			if err != nil {
				return nil, fmt.Errorf("decrypt %s fallback[%d] key: %w", provider, i, err)
			}
			key = dec
		}
		mdl := fb.Model
		if mdl == "" {
			mdl = model // switchToNextEndpoint keeps current model when blank
		}
		targets = append(targets, probeTarget{
			Provider: provider,
			Role:     fmt.Sprintf("try#%d", i+1), // 1-based attempt order after primary
			Name:     fb.Name,
			BaseURL:  fb.BaseURL,
			Model:    mdl,
			APIKey:   key,
			KeySrc:   src,
			Priority: fb.Priority,
		})
	}
	return targets, nil
}

// probe issues one minimal real call through the production provider client.
func probe(t probeTarget, timeout time.Duration) (string, time.Duration, error) {
	client := mcp.NewAIClientByProvider(t.Provider)
	if client == nil {
		return "", 0, fmt.Errorf("provider %s not registered", t.Provider)
	}
	client.SetAPIKey(t.APIKey, t.BaseURL, t.Model)
	if bc, ok := client.(interface{ BaseClient() *mcp.Client }); ok {
		base := bc.BaseClient()
		base.SetTimeout(timeout)
		base.Cfg.MaxRetries = 0 // one shot per endpoint; we want per-endpoint truth
	}
	start := time.Now()
	out, err := client.CallWithMessages(
		"You are a connectivity probe. Reply with exactly: OK",
		"Reply with exactly: OK",
	)
	return out, time.Since(start), err
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "path to data.db (opened read-only)")
	envFile := flag.String("env", "/opt/webstack/nofx/.env", "path to .env providing DATA_ENCRYPTION_KEY + RSA_PRIVATE_KEY")
	providers := flag.String("providers", "claude,openai", "comma-separated provider names to probe")
	timeout := flag.Duration("timeout", 60*time.Second, "per-endpoint timeout")
	dryRun := flag.Bool("dry-run", false, "resolve and print the endpoint matrix without making network calls")
	// Cross-test: borrow a key that is known to work from one row and use it
	// against a different provider/endpoint. This distinguishes "the token is
	// bad" from "this relay does not speak this provider's wire format".
	keyFrom := flag.String("key-from", "", "borrow key from <provider>:<primary|N> where N is the 1-based try order, e.g. openai:2")
	asProvider := flag.String("as-provider", "", "probe the borrowed key as this provider (claude|openai|...)")
	asBaseURL := flag.String("as-base-url", "", "base URL for the cross-test")
	asModel := flag.String("as-model", "", "model for the cross-test")
	flag.Parse()

	if *envFile != "" {
		if err := godotenv.Load(*envFile); err != nil {
			fmt.Printf("warn: could not load %s: %v\n", *envFile, err)
		}
	}
	cs, err := crypto.NewCryptoService()
	if err != nil {
		fmt.Printf("FATAL crypto init: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro")
	if err != nil {
		fmt.Printf("FATAL open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var all []probeTarget
	for _, p := range strings.Split(*providers, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ts, err := loadTargets(db, cs, p)
		if err != nil {
			fmt.Printf("FATAL load %s: %v\n", p, err)
			os.Exit(1)
		}
		all = append(all, ts...)
	}

	// Cross-test mode replaces the normal matrix entirely.
	if *keyFrom != "" {
		parts := strings.SplitN(*keyFrom, ":", 2)
		if len(parts) != 2 || *asProvider == "" || *asBaseURL == "" || *asModel == "" {
			fmt.Println("FATAL -key-from requires -as-provider, -as-base-url and -as-model")
			os.Exit(1)
		}
		srcTargets, err := loadTargets(db, cs, parts[0])
		if err != nil {
			fmt.Printf("FATAL load %s: %v\n", parts[0], err)
			os.Exit(1)
		}
		var borrowed string
		if parts[1] == "primary" {
			borrowed = srcTargets[0].APIKey
		} else {
			want := "try#" + parts[1]
			for _, t := range srcTargets {
				if t.Role == want {
					borrowed = t.APIKey
				}
			}
		}
		if borrowed == "" {
			fmt.Printf("FATAL could not resolve key %s\n", *keyFrom)
			os.Exit(1)
		}
		t := probeTarget{
			Provider: *asProvider,
			Role:     "cross-test",
			Name:     "borrowed:" + *keyFrom,
			BaseURL:  *asBaseURL,
			Model:    *asModel,
			APIKey:   borrowed,
			KeySrc:   "borrowed:" + *keyFrom,
		}
		fmt.Printf("cross-test: key %s from %s -> %s %s %s\n\n",
			mask(borrowed), *keyFrom, *asProvider, *asBaseURL, *asModel)
		out, dur, err := probe(t, *timeout)
		if err != nil {
			fmt.Printf("[FAIL] %6.2fs err: %s\n", dur.Seconds(), strings.ReplaceAll(err.Error(), "\n", " "))
			os.Exit(2)
		}
		fmt.Printf("[OK  ] %6.2fs reply=%q\n", dur.Seconds(), strings.TrimSpace(out))
		return
	}

	fmt.Printf("resolved %d endpoint(s)\n\n", len(all))
	for _, t := range all {
		fmt.Printf("%-8s %-12s prio=%-3d %-28s model=%-32s key=%s (%s)\n",
			t.Provider, t.Role, t.Priority, t.BaseURL, t.Model, mask(t.APIKey), t.KeySrc)
	}
	if *dryRun {
		fmt.Println("\ndry-run: no network calls made")
		return
	}

	fmt.Printf("\n--- probing (timeout %s, no retries) ---\n\n", *timeout)
	okCount := 0
	type result struct {
		t   probeTarget
		err error
		dur time.Duration
		out string
	}
	var results []result
	for _, t := range all {
		out, dur, err := probe(t, *timeout)
		results = append(results, result{t, err, dur, out})
		status := "OK  "
		if err != nil {
			status = "FAIL"
		} else {
			okCount++
		}
		fmt.Printf("[%s] %-8s %-12s %-28s %-32s %6.2fs",
			status, t.Provider, t.Role, t.BaseURL, t.Model, dur.Seconds())
		if err != nil {
			msg := err.Error()
			if len(msg) > 260 {
				msg = msg[:260] + "..."
			}
			fmt.Printf("\n         err: %s\n", strings.ReplaceAll(msg, "\n", " "))
		} else {
			reply := strings.TrimSpace(out)
			if len(reply) > 60 {
				reply = reply[:60] + "..."
			}
			fmt.Printf("  reply=%q\n", reply)
		}
	}

	fmt.Printf("\n=== summary: %d/%d usable ===\n", okCount, len(all))
	for _, p := range strings.Split(*providers, ",") {
		p = strings.TrimSpace(p)
		var tot, ok int
		for _, r := range results {
			if r.t.Provider != p {
				continue
			}
			tot++
			if r.err == nil {
				ok++
			}
		}
		if tot > 0 {
			fmt.Printf("  %-8s %d/%d usable\n", p, ok, tot)
		}
	}
	if okCount < len(all) {
		os.Exit(2)
	}
}
