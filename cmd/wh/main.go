// wh — Wormhole CLI helper.
//
// Local route: points at any Wormhole server (default localhost:8080)
// and uses your own key — or no key at all (anonymous keyless pool).
//
//	wh models                          list models on the server
//	wh chat "prompt"                   chat (streaming), keyless by default
//	wh chat "prompt" -model kilo/nemotron-3-nano -key sk-wormhole-...
//	wh keys list | create <name>       manage keys (local server only)
//	wh usage                           7-day totals
//
// Env: WH_SERVER, WH_KEY, WH_MODEL
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 120 * time.Second}

func usage() {
	fmt.Fprintln(os.Stderr, `wh — Wormhole CLI helper

  wh models                        list models on the server
  wh chat "prompt"                   chat with a model (streams by default)
  wh keys list                     list API keys
  wh keys create <name>            create an API key
  wh usage                         7-day usage totals

Flags: -server URL (env WH_SERVER, default http://localhost:8080)
       -key KEY    (env WH_KEY, optional — anonymous pool otherwise)
       -model ID   (env WH_MODEL, e.g. pollinations/openai-fast)`)
}

func serverBase(fs *flag.FlagSet) string {
	s := fs.String("server", envOr("WH_SERVER", "http://localhost:8080"), "router server URL")
	return strings.TrimRight(*s, "/")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func getJSON(url, key string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "models":
		err = cmdModels(os.Args[2:])
	case "chat":
		err = cmdChat(os.Args[2:])
	case "keys":
		err = cmdKeys(os.Args[2:])
	case "usage":
		err = cmdUsage(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	base := serverBase(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var list struct {
		Data []struct {
			ID      string   `json:"id"`
			OwnedBy string   `json:"owned_by"`
			Tags    []string `json:"tags"`
		} `json:"data"`
	}
	if err := getJSON(base+"/v1/models", "", &list); err != nil {
		return err
	}
	w := 0
	for _, m := range list.Data {
		if len(m.ID) > w {
			w = len(m.ID)
		}
	}
	for _, m := range list.Data {
		fmt.Printf("%-*s  %-12s  %s\n", w, m.ID, m.OwnedBy, strings.Join(m.Tags, ","))
	}
	return nil
}

func cmdUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	base := serverBase(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var u struct {
		Totals struct {
			Requests  int     `json:"requests"`
			CostUSD   float64 `json:"cost_usd"`
		} `json:"totals"`
	}
	if err := getJSON(base+"/api/usage", "", &u); err != nil {
		return err
	}
	fmt.Printf("requests (7d): %d   spend: $%.4f\n", u.Totals.Requests, u.Totals.CostUSD)
	return nil
}

func cmdKeys(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wh keys list | create <name> [-limit N]")
	}
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	base := serverBase(fs)
	limit := fs.Float64("limit", 0, "monthly spend limit in USD")
	// parse flags from the tail (after subcommand)
	rest := []string{}
	name := ""
	if len(args) > 1 {
		name = args[1]
		rest = args[2:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		var keys []struct {
			Name         string  `json:"name"`
			Prefix       string  `json:"prefix"`
			MonthlyLimit float64 `json:"monthly_limit"`
			MonthSpend   float64 `json:"month_spend"`
		}
		if err := getJSON(base+"/api/keys", "", &keys); err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Println("no keys yet")
			return nil
		}
		for _, k := range keys {
			fmt.Printf("%-20s %-18s $%.4f / $%.2f\n", k.Name, k.Prefix, k.MonthSpend, k.MonthlyLimit)
		}
	case "create":
		if name == "" {
			return fmt.Errorf("usage: wh keys create <name> [-limit N]")
		}
		body, _ := json.Marshal(map[string]any{"name": name, "monthly_limit": *limit})
		resp, err := httpClient.Post(base+"/api/keys", "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var out struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
			return err
		}
		fmt.Println(out.Key)
	default:
		return fmt.Errorf("usage: wh keys list | create <name>")
	}
	return nil
}

func cmdChat(args []string) error {
	base := strings.TrimRight(envOr("WH_SERVER", "http://localhost:8080"), "/")
	key := envOr("WH_KEY", "")
	model := envOr("WH_MODEL", "pollinations/openai-fast")
	system := ""
	plain := false
	msgParts := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "-model" || a == "--model":
			model = next()
		case a == "-key" || a == "--key":
			key = next()
		case a == "-server" || a == "--server":
			base = strings.TrimRight(next(), "/")
		case a == "-system" || a == "--system":
			system = next()
		case a == "-no-stream" || a == "--no-stream":
			plain = true
		default:
			msgParts = append(msgParts, a)
		}
	}
	msg := strings.Join(msgParts, " ")
	if msg == "" {
		return fmt.Errorf("usage: wh chat \"prompt\" [-model id]")
	}
	msgs := []map[string]string{}
	if system != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": msg})
	body, _ := json.Marshal(map[string]any{
		"model": model, "messages": msgs, "stream": !plain,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("%s (status %d)", e.Error.Message, resp.StatusCode)
	}
	if plain {
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		if len(out.Choices) > 0 {
			fmt.Println(out.Choices[0].Message.Content)
		}
		return nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	prefix := "data" + ":"
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		fmt.Print(sseContent(payload))
	}
	fmt.Println()
	return sc.Err()
}

// sseContent extracts choices[0].delta.content from one SSE data line.
func sseContent(payload string) string {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return ""
	}
	out := ""
	for _, c := range chunk.Choices {
		out += c.Delta.Content
	}
	return out
}
