// Command selfdoubtprobe sends crafted market scenarios to the live claude and
// gpt endpoints (decrypted from data.db, same wire format the traders use) under
// the REAL runtime system prompt, and captures each model's full reasoning +
// decision. Purpose: detect where the AI SELF-DENIES a valid opportunity using a
// stricter-than-gate standard (the failure mode we're hunting), vs. correctly
// proposing and letting the backend gate decide.
//
// Keys are decrypted in-process and never printed. Read-only DB access.
//
//	go run ./cmd/selfdoubtprobe -db <data.db> -sys <system_prompt.txt> \
//	    -scenarios <dir> -out <dir> [-provider claude|openai|both] [-max-tokens N]
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
	"path/filepath"
	"sort"
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

type respContent struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func buildURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// loadEndpoint decrypts the primary key for one provider from ai_models.
func loadEndpoint(dbPath, provider string) (endpoint, error) {
	cs, err := crypto.NewCryptoService()
	if err != nil {
		return endpoint{}, fmt.Errorf("crypto init (need DATA_ENCRYPTION_KEY + RSA_PRIVATE_KEY): %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return endpoint{}, err
	}
	defer db.Close()
	var encKey, apiURL, model string
	row := db.QueryRow(`SELECT api_key, custom_api_url, custom_model_name
		FROM ai_models WHERE provider=? AND enabled=1 LIMIT 1`, provider)
	if err := row.Scan(&encKey, &apiURL, &model); err != nil {
		return endpoint{}, fmt.Errorf("query %s model: %w", provider, err)
	}
	key, err := cs.DecryptFromStorage(encKey)
	if err != nil {
		return endpoint{}, fmt.Errorf("decrypt %s key: %w", provider, err)
	}
	return endpoint{provider, apiURL, key, model}, nil
}

func call(client *http.Client, e endpoint, sys, user string, maxTokens int) (string, respContent, error) {
	body := map[string]any{
		"model": e.model,
		"messages": []map[string]string{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
		},
		"max_tokens":  maxTokens,
		"temperature": 0.0,
	}
	jsonData, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", buildURL(e.url), bytes.NewBuffer(jsonData))
	if err != nil {
		return "", respContent{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := client.Do(req)
	if err != nil {
		return "", respContent{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", respContent{}, fmt.Errorf("status %d: %s", resp.StatusCode, preview(raw, 300))
	}
	var rc respContent
	if err := json.Unmarshal(raw, &rc); err != nil {
		return "", respContent{}, fmt.Errorf("parse: %w | %s", err, preview(raw, 200))
	}
	text := ""
	if len(rc.Choices) > 0 {
		m := rc.Choices[0].Message
		switch {
		case m.Content != "":
			text = m.Content
		case m.ReasoningContent != "":
			text = "[reasoning_content]\n" + m.ReasoningContent
		case m.Reasoning != "":
			text = "[reasoning]\n" + m.Reasoning
		}
	}
	if text == "" {
		text = "<empty> raw=" + preview(raw, 300)
	}
	return text, rc, nil
}

func preview(b []byte, limit int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

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

func main() {
	dbPath := flag.String("db", "", "path to data.db (read-only use)")
	sysPath := flag.String("sys", "", "path to system prompt text file")
	scenDir := flag.String("scenarios", "", "dir of *.txt user-prompt scenario files")
	outDir := flag.String("out", "/tmp/selfdoubt/out", "output dir")
	provider := flag.String("provider", "both", "claude|openai|both")
	maxTokens := flag.Int("max-tokens", 3000, "max_tokens for reply")
	timeout := flag.Duration("timeout", 180*time.Second, "per-request timeout")
	envFile := flag.String("env", "", "path to .env to load (DATA_ENCRYPTION_KEY, RSA_PRIVATE_KEY) before decrypting")
	flag.Parse()

	if *envFile != "" {
		loadDotEnv(*envFile)
	}

	if *dbPath == "" || *sysPath == "" || *scenDir == "" {
		fmt.Println("need -db, -sys, -scenarios")
		os.Exit(2)
	}
	sysBytes, err := os.ReadFile(*sysPath)
	if err != nil {
		fmt.Println("read sys:", err)
		os.Exit(1)
	}
	sys := string(sysBytes)

	var providers []string
	switch *provider {
	case "both":
		providers = []string{"claude", "openai"}
	default:
		providers = []string{*provider}
	}
	eps := map[string]endpoint{}
	for _, p := range providers {
		ep, err := loadEndpoint(*dbPath, p)
		if err != nil {
			fmt.Printf("load %s endpoint: %v\n", p, err)
			os.Exit(1)
		}
		eps[p] = ep
		fmt.Printf("loaded %s: model=%s url=%s (key len=%d)\n", p, ep.model, ep.url, len(ep.key))
	}

	files, _ := filepath.Glob(filepath.Join(*scenDir, "*.txt"))
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Println("no scenario files in", *scenDir)
		os.Exit(1)
	}
	os.MkdirAll(*outDir, 0o755)
	client := &http.Client{Timeout: *timeout}

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		userBytes, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("[%s] read: %v\n", name, err)
			continue
		}
		user := string(userBytes)
		for _, p := range providers {
			ep := eps[p]
			fmt.Printf("→ %s / %s ... ", name, p)
			t0 := time.Now()
			text, rc, err := call(client, ep, sys, user, *maxTokens)
			dt := time.Since(t0).Round(time.Millisecond)
			if err != nil {
				fmt.Printf("ERROR %v\n", err)
				os.WriteFile(filepath.Join(*outDir, name+"."+p+".ERROR.txt"), []byte(err.Error()), 0o644)
				continue
			}
			fmt.Printf("ok %s prompt=%d completion=%d\n", dt, rc.Usage.PromptTokens, rc.Usage.CompletionTokens)
			out := fmt.Sprintf("=== scenario=%s provider=%s model=%s latency=%s prompt_tok=%d completion_tok=%d ===\n\n%s\n",
				name, p, rc.Model, dt, rc.Usage.PromptTokens, rc.Usage.CompletionTokens, strings.TrimSpace(text))
			os.WriteFile(filepath.Join(*outDir, name+"."+p+".txt"), []byte(out), 0o644)
		}
	}
	fmt.Println("done →", *outDir)
}
