// Command aicacheprobe fires an identical payload at two OpenAI-compatible
// endpoints (e.g. novai + ltcraft) and prints precise UTC timestamps plus the
// token-usage breakdown returned by each, so the two providers' server-side
// logs can be correlated against a controlled request.
//
// It mirrors the exact wire format the traders use (see mcp.Client):
//
//	POST {base}/chat/completions
//	Authorization: Bearer <key>
//	{"model":..,"messages":[{system},{user}],"temperature":..,"max_tokens":..}
//
// Usage (endpoints are supplied via env so no secrets touch the source tree):
//
//	NOVAI_URL=https://once.novai.su/v1 NOVAI_KEY=sk-... NOVAI_MODEL=claude-... \
//	LT_URL=https://ltcraft.example/v1  LT_KEY=sk-...    LT_MODEL=claude-... \
//	go run ./cmd/aicacheprobe -sys-kb 16 -user-kb 4 -repeat 2 -gap 3s
//
// The system prompt is held BYTE-IDENTICAL across every call (that is what a
// prompt cache keys on); only the user message carries a per-call nonce so the
// providers cannot dedupe the whole request. Run -repeat 2 to see whether the
// second call registers a cache hit (novai) or not (ltcraft).
package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"nofx/crypto"

	_ "modernc.org/sqlite"
)

type endpoint struct {
	label string
	url   string
	key   string
	model string
}

// usageProbe captures every cache/usage field shape observed across providers
// (see mcp/client.go ParseMCPResponseFull comment block).
type usageProbe struct {
	Usage struct {
		PromptTokens         int `json:"prompt_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
		TotalTokens          int `json:"total_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"` // Anthropic-native
		CachedTokens         int `json:"cached_tokens"`           // some proxies
		PromptTokensDetails  struct {
			CachedTokens int `json:"cached_tokens"` // novai (once.novai.su)
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (u usageProbe) cacheRead() (int, string) {
	switch {
	case u.Usage.PromptTokensDetails.CachedTokens > 0:
		return u.Usage.PromptTokensDetails.CachedTokens, "prompt_tokens_details.cached_tokens"
	case u.Usage.CacheReadInputTokens > 0:
		return u.Usage.CacheReadInputTokens, "cache_read_input_tokens"
	case u.Usage.CachedTokens > 0:
		return u.Usage.CachedTokens, "cached_tokens"
	}
	return 0, "(none reported)"
}

func buildURL(base string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "#")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/chat/completions"
}

// filler returns a deterministic, incompressible-enough block of ~kb kilobytes
// so the token count is stable and comparable across runs.
func filler(kb int, seed string) string {
	const line = "MARKET-CONTEXT %s | candle o/h/l/c volume oi funding delta cvd liq depth spread |"
	var b strings.Builder
	target := kb * 1024
	i := 0
	for b.Len() < target {
		fmt.Fprintf(&b, line+" row=%06d\n", seed, i)
		i++
	}
	return b.String()
}

func main() {
	sysKB := flag.Int("sys-kb", 16, "approx size of the (cache-eligible, identical) system prompt in KB")
	userKB := flag.Int("user-kb", 4, "approx size of the per-call user message in KB")
	repeat := flag.Int("repeat", 2, "calls per endpoint (>=2 to observe a warm-cache hit)")
	gap := flag.Duration("gap", 3*time.Second, "delay between calls to the same endpoint")
	maxTokens := flag.Int("max-tokens", 64, "max_tokens for the reply (keep small; we only care about input accounting)")
	timeout := flag.Duration("timeout", 90*time.Second, "per-request HTTP timeout")
	fromDB := flag.String("from-db", "", "path to sqlite data.db: load the claude model's primary (lt) + fallback (novai) endpoints and decrypt keys in-process")
	envFile := flag.String("env", "", "path to a .env file to load DATA_ENCRYPTION_KEY/RSA_PRIVATE_KEY before decrypting")
	fixedUser := flag.Bool("fixed-user", false, "send a byte-identical user message every call (no per-call nonce) to test whether a warm cache read ever triggers")
	only := flag.String("only", "", "restrict to a single endpoint label (e.g. novai or lt)")
	hdrs := flag.String("hdr", "", "extra request headers, comma-separated K:V (e.g. 'X-No-Cache:1,Cache-Control:no-store')")
	bodyFlags := flag.String("body-flags", "", "extra top-level JSON body flags as k=v pairs, comma-separated. bool/number auto-typed (e.g. 'disable_cache=true,cache=false')")
	scenario := flag.String("scenario", "", "faithful prompt-structure test: 'prod' (volatile equity embedded mid system-prompt) | 'fixed' (equity moved to user tail, system byte-identical)")
	realFile := flag.String("real-file", "", "load real prompt text from this file (repeated to reach -real-target chars) instead of synthetic filler")
	realTarget := flag.Int("real-target", 0, "target character count when using -real-file (repeats content to reach it)")
	modelOverride := flag.String("model", "", "override the model name for all endpoints (e.g. claude-opus-4-7, claude-sonnet-4-6)")
	ask := flag.String("ask", "", "fingerprint mode: send this question to each endpoint and print the full text reply (for model-identity comparison)")
	noTemp := flag.Bool("no-temp", false, "omit the temperature field (some models reject it)")
	ltAlt := flag.String("lt-alt", "", "use a non-claude provider's ltcraft key instead of the claude one (e.g. 'openai' or 'qwen'); needs -from-db")
	flag.Parse()

	var eps []endpoint
	if *fromDB != "" {
		if *envFile != "" {
			loadDotEnv(*envFile)
		}
		loaded, err := loadEndpointsFromDB(*fromDB)
		if err != nil {
			fmt.Printf("from-db load failed: %v\n", err)
			os.Exit(2)
		}
		eps = loaded
		if *ltAlt != "" {
			altURL, altKey, aerr := loadLtcraftAltKey(*fromDB, *ltAlt)
			if aerr != nil {
				fmt.Printf("lt-alt load failed: %v\n", aerr)
				os.Exit(2)
			}
			// Replace the lt endpoint's key with the alt provider's ltcraft key.
			for i := range eps {
				if eps[i].label == "lt" {
					eps[i].url = altURL
					eps[i].key = altKey
				}
			}
			fmt.Printf("(lt-alt: using %s provider's ltcraft key)\n", *ltAlt)
		}
	} else {
		eps = []endpoint{
			{"novai", os.Getenv("NOVAI_URL"), os.Getenv("NOVAI_KEY"), envOr("NOVAI_MODEL", "claude-opus-4-8")},
			{"lt", os.Getenv("LT_URL"), os.Getenv("LT_KEY"), envOr("LT_MODEL", "claude-opus-4-8")},
		}
	}

	active := eps[:0]
	for i := range eps {
		if *modelOverride != "" {
			eps[i].model = *modelOverride
		}
	}
	for _, e := range eps {
		if *only != "" && e.label != *only {
			continue
		}
		if e.url != "" && e.key != "" {
			active = append(active, e)
		} else {
			fmt.Printf("… skipping %-6s (set %s_URL and %s_KEY to enable)\n",
				e.label, strings.ToUpper(e.label), strings.ToUpper(e.label))
		}
	}
	if len(active) == 0 {
		fmt.Println("no endpoints configured; set NOVAI_URL/NOVAI_KEY and/or LT_URL/LT_KEY")
		os.Exit(2)
	}

	// Identical system prompt across every call — this is the cache key.
	sysPrompt := "You are a trading assistant. Reply with the single word OK.\n" + filler(*sysKB, "SYS")
	if *realFile != "" {
		sysPrompt = loadRealPrompt(*realFile, *realTarget)
		fmt.Printf("(real-file loaded: %s → %d bytes)\n", *realFile, len(sysPrompt))
	}

	client := &http.Client{Timeout: *timeout}

	// Fingerprint mode: ask an identical question to each endpoint and print the
	// full text reply so model identity/behaviour can be compared side by side.
	if *ask != "" {
		fmt.Printf("=== fingerprint | model=%s ===\nQ: %s\n\n", firstModel(active), *ask)
		for _, e := range active {
			askOnce(client, e, *ask, *maxTokens, *noTemp)
			fmt.Println()
		}
		return
	}

	fmt.Printf("=== aicacheprobe | sys≈%dKB user≈%dKB repeat=%d gap=%s ===\n", *sysKB, *userKB, *repeat, *gap)
	fmt.Printf("system-prompt bytes=%d (byte-identical every call)\n\n", len(sysPrompt))

	for _, e := range active {
		fmt.Printf("---- endpoint=%s model=%s url=%s ----\n", e.label, e.model, buildURL(e.url))
		for n := 1; n <= *repeat; n++ {
			// Scenario mode overrides sysPrompt+user to mirror the real trader prompt.
			if *scenario != "" {
				sp, up := buildScenario(*scenario, *sysKB, n)
				callOnce(client, e, sp, up, *maxTokens, n, *hdrs, *bodyFlags)
				if n < *repeat {
					time.Sleep(*gap)
				}
				continue
			}
			var userMsg string
			if os.Getenv("PREFIX_STABLE") == "1" {
				// Stable prefix + tiny changing SUFFIX: tests whether a longest-common-prefix
				// cache read triggers when only the trailing (market-data-like) line changes.
				userMsg = "REQUEST prefix-probe\n" + filler(*userKB, "STABLEPREFIX") +
					fmt.Sprintf("\nLIVE-TICK %d-%d\n", n, time.Now().UnixNano())
			} else if *fixedUser {
				// Byte-identical every call: maximises chance of a warm-cache READ.
				userMsg = "REQUEST fixed-probe\n" + filler(*userKB, "FIXED")
			} else {
				// Only the nonce changes; bulk of the user body is stable filler.
				nonce := fmt.Sprintf("%s-call%d-%d", e.label, n, time.Now().UnixNano())
				userMsg = "REQUEST " + nonce + "\n" + filler(*userKB, nonce)
			}
			callOnce(client, e, sysPrompt, userMsg, *maxTokens, n, *hdrs, *bodyFlags)
			if n < *repeat {
				time.Sleep(*gap)
			}
		}
		fmt.Println()
	}
	fmt.Println("Done. Correlate the UTC send timestamps above with each provider's request log.")
}

func firstModel(eps []endpoint) string {
	if len(eps) > 0 {
		return eps[0].model
	}
	return "?"
}

// respContent parses the assistant text reply from an OpenAI-compatible response,
// covering the same content/reasoning fallback fields mcp/client.go handles.
type respContent struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
}

// askOnce sends a single question and prints the full text reply + usage, so the
// same prompt can be compared across endpoints for model-identity fingerprinting.
func askOnce(client *http.Client, e endpoint, question string, maxTokens int, noTemp bool) {
	body := map[string]any{
		"model": e.model,
		"messages": []map[string]string{
			{"role": "user", "content": question},
		},
		"max_tokens": maxTokens,
	}
	if !noTemp {
		body["temperature"] = 0.0
	}
	jsonData, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", buildURL(e.url), bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("  [%s] build error: %v\n", e.label, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)

	sent := time.Now().UTC()
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  [%s] ERROR %v\n", e.label, err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	latency := time.Since(sent).Round(time.Millisecond)

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  [%s] non-200: %s\n", e.label, preview(respBody, 300))
		return
	}

	var rc respContent
	json.Unmarshal(respBody, &rc)
	var u usageProbe
	json.Unmarshal(respBody, &u)

	text := ""
	if len(rc.Choices) > 0 {
		m := rc.Choices[0].Message
		switch {
		case m.Content != "":
			text = m.Content
		case m.ReasoningContent != "":
			text = "[reasoning_content] " + m.ReasoningContent
		case m.Reasoning != "":
			text = "[reasoning] " + m.Reasoning
		}
	}
	if text == "" {
		text = "<empty> raw=" + preview(respBody, 200)
	}

	fmt.Printf("  [%s] model=%s latency=%s prompt=%d completion=%d\n", e.label, rc.Model, latency, u.Usage.PromptTokens, u.Usage.CompletionTokens)
	fmt.Printf("  [%s] REPLY: %s\n", e.label, strings.TrimSpace(text))
}

func callOnce(client *http.Client, e endpoint, sys, user string, maxTokens, n int, hdrs, bodyFlags string) {
	body := map[string]any{
		"model": e.model,
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
		},
		"max_tokens": maxTokens,
	}
	if os.Getenv("NO_TEMPERATURE") != "1" {
		body["temperature"] = 0.0
	}
	for k, v := range parseBodyFlags(bodyFlags) {
		body[k] = v
	}
	jsonData, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", buildURL(e.url), bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("  [%s #%d] build error: %v\n", e.label, n, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)
	for k, v := range parseHeaders(hdrs) {
		req.Header.Set(k, v)
	}

	sent := time.Now().UTC()
	fmt.Printf("  [%s #%d] SENT   %s  reqBytes=%d\n",
		e.label, n, sent.Format("2006-01-02 15:04:05.000 MST"), len(jsonData))

	resp, err := client.Do(req)
	recv := time.Now().UTC()
	if err != nil {
		fmt.Printf("  [%s #%d] ERROR  %s  %v\n", e.label, n, recv.Format("15:04:05.000"), err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	fmt.Printf("  [%s #%d] RECV   %s  status=%d latency=%s\n",
		e.label, n, recv.Format("2006-01-02 15:04:05.000 MST"), resp.StatusCode, recv.Sub(sent).Round(time.Millisecond))

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  [%s #%d] non-200 body: %s\n", e.label, n, preview(respBody, 300))
		return
	}

	var u usageProbe
	if err := json.Unmarshal(respBody, &u); err != nil {
		fmt.Printf("  [%s #%d] usage parse failed: %v | %s\n", e.label, n, err, preview(respBody, 200))
		return
	}
	cached, field := u.cacheRead()
	fresh := u.Usage.PromptTokens - cached
	fmt.Printf("  [%s #%d] USAGE  prompt=%d (cached=%d via %s, fresh=%d) completion=%d total=%d\n",
		e.label, n, u.Usage.PromptTokens, cached, field, fresh, u.Usage.CompletionTokens, u.Usage.TotalTokens)
}

func preview(b []byte, limit int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > limit {
		return s[:limit] + "..."
	}
	if s == "" {
		return "<empty>"
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildScenario mirrors the real trader prompt layout to test the caching fix.
//
// The production system prompt (kernel/engine_prompt.go BuildSystemPrompt) is a
// large mostly-static block, but ~line 152 it interpolates volatile equity-derived
// numbers, then continues with more static text. So its structure is:
//
//	[static head] + [VOLATILE equity block] + [static tail]
//
// Anthropic prompt caching matches the longest byte-identical PREFIX. The volatile
// equity block sits mid-prompt, so the identical prefix ends at the equity block —
// everything after it (the large static tail) cannot be cached. Hence prod rewrites
// almost the whole prompt every call.
//
//	scenario=prod  → head + changing-equity + tail  (equity mid-prompt, like today)
//	scenario=fixed → head + tail (byte-identical); equity numbers moved to USER tail
//
// The changing equity value simulates account PnL drift between scan cycles.
func buildScenario(mode string, sysKB, n int) (system, user string) {
	// Split the static system content into a head and tail around the equity block,
	// echoing the real prompt where equity lands roughly a quarter of the way in.
	headKB := sysKB / 4
	if headKB < 1 {
		headKB = 1
	}
	tailKB := sysKB - headKB
	if tailKB < 1 {
		tailKB = 1
	}
	head := "You are a professional crypto trading AI. Constraints and schema follow.\n" + filler(headKB, "HEAD")
	tail := "\n## Trading Principles / Schema / Output Contract\n" + filler(tailKB, "TAIL")

	// Volatile equity value — drifts each scan cycle to mimic real PnL movement.
	equity := 40.0 + float64(n)*0.137
	equityBlock := fmt.Sprintf(
		"\n## Hard Constraints\n"+
			"- Position Value Limit (Altcoins): max %.0f USDT (= equity %.2f x 3.0)\n"+
			"- Position Value Limit (BTC/ETH): max %.0f USDT (= equity %.2f x 5.0)\n"+
			"- Example: With equity %.2f and BTC/ETH ratio 5.0x, max is %.0f USDT\n",
		equity*3, equity, equity*5, equity, equity, equity*5)

	// Market data — always changes every scan (the genuinely fresh part).
	marketData := fmt.Sprintf("\n## Live Market Snapshot (scan %d @ %d)\n", n, time.Now().UnixNano()) +
		filler(2, "MARKET") + fmt.Sprintf("\nBTC last=%d ts=%d\n", 60000+n, time.Now().UnixNano())

	switch mode {
	case "prod":
		// Volatile equity embedded mid system-prompt → prefix breaks at the equity block.
		system = head + equityBlock + tail
		user = "Analyze and decide." + marketData
	case "fixed":
		// System is byte-identical every call; equity relocated to the USER tail,
		// AFTER the market data so the whole system block stays a cacheable prefix.
		system = head + tail
		user = "Analyze and decide." + marketData +
			"\n## Account Snapshot (this cycle)" + equityBlock
	default:
		fmt.Printf("unknown scenario %q (use prod|fixed)\n", mode)
		os.Exit(2)
	}
	return system, user
}

// loadRealPrompt reads real prompt text and repeats it (with a section marker so
// each copy differs slightly, defeating trivial dedup) until it reaches target chars.
// This reproduces real token DENSITY (Chinese + JSON + numbers), unlike synthetic
// English filler which the tokenizer over-compresses.
func loadRealPrompt(path string, targetChars int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("cannot read real-file %s: %v\n", path, err)
		os.Exit(2)
	}
	base := string(raw)
	if targetChars <= 0 || len([]rune(base)) >= targetChars {
		return base
	}
	var b strings.Builder
	i := 0
	for len([]rune(b.String())) < targetChars {
		fmt.Fprintf(&b, "\n\n## 段落 %d\n", i)
		b.WriteString(base)
		i++
	}
	return b.String()
}

func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}

func parseBodyFlags(s string) map[string]any {
	out := map[string]any{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		switch v {
		case "true":
			out[k] = true
		case "false":
			out[k] = false
		default:
			out[k] = v
		}
	}
	return out
}

// loadDotEnv loads KEY=VALUE lines into the process env (only if not already set).
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("warn: cannot open env file %s: %v\n", path, err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

type fbEndpoint struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// loadLtcraftAltKey decrypts a NON-claude provider key that also points at
// ai.ltcraft.cn (e.g. openai/gpt-5.5 or qwen). Useful when the claude key has
// no available credentials on the relay but another ltcraft key still works.
// Returns (baseURL, key). Key held in memory only, never printed.
func loadLtcraftAltKey(dbPath, provider string) (string, string, error) {
	cs, err := crypto.NewCryptoService()
	if err != nil {
		return "", "", fmt.Errorf("crypto init: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	var encKey, apiURL string
	row := db.QueryRow(`SELECT api_key, custom_api_url FROM ai_models
		WHERE provider=? AND enabled=1 AND custom_api_url LIKE '%ltcraft%' LIMIT 1`, provider)
	if err := row.Scan(&encKey, &apiURL); err != nil {
		return "", "", fmt.Errorf("query %s ltcraft row: %w", provider, err)
	}
	key, err := cs.DecryptFromStorage(encKey)
	if err != nil {
		return "", "", fmt.Errorf("decrypt %s key: %w", provider, err)
	}
	return apiURL, key, nil
}

// loadEndpointsFromDB reads the claude ai_model row, decrypts the primary key
// (lt / ltcraft) and the first fallback key (novai), returning both endpoints.
// Keys are held only in memory and never printed.
func loadEndpointsFromDB(dbPath string) ([]endpoint, error) {
	cs, err := crypto.NewCryptoService()
	if err != nil {
		return nil, fmt.Errorf("crypto init (need DATA_ENCRYPTION_KEY + RSA_PRIVATE_KEY in env): %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var encKey, apiURL, model, fbJSON string
	row := db.QueryRow(`SELECT api_key, custom_api_url, custom_model_name, fallback_endpoints
		FROM ai_models WHERE provider='claude' AND enabled=1 LIMIT 1`)
	if err := row.Scan(&encKey, &apiURL, &model, &fbJSON); err != nil {
		return nil, fmt.Errorf("query claude model: %w", err)
	}

	ltKey, err := cs.DecryptFromStorage(encKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt lt primary key: %w", err)
	}
	if model == "" {
		model = "claude-opus-4-8"
	}
	eps := []endpoint{{"lt", apiURL, ltKey, model}}

	if strings.TrimSpace(fbJSON) != "" {
		var fbs []fbEndpoint
		if err := json.Unmarshal([]byte(fbJSON), &fbs); err != nil {
			return nil, fmt.Errorf("parse fallback json: %w", err)
		}
		for _, fb := range fbs {
			key := fb.APIKey
			// Fallback keys may be stored encrypted or plain depending on save path.
			if cs.IsEncryptedStorageValue(key) {
				if dec, derr := cs.DecryptFromStorage(key); derr == nil {
					key = dec
				}
			}
			m := fb.Model
			if m == "" {
				m = model
			}
			eps = append(eps, endpoint{"novai", fb.BaseURL, key, m})
		}
	}
	return eps, nil
}
