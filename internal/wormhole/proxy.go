package wormhole

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Proxy routes a chat completion to the single provider configured for the
// requested model — like OpenRouter: one request in, the right upstream out.
type Proxy struct {
	cfg   *Config
	keys  *KeyStore
	usage *UsageStore
	http  *http.Client
}

func NewProxy(cfg *Config, keys *KeyStore, usage *UsageStore) *Proxy {
	return &Proxy{cfg: cfg, keys: keys, usage: usage, http: &http.Client{Timeout: 180 * time.Second}}
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
	Stream   bool      `json:"stream"`
}

type upResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// Call handles POST /v1/chat/completions.
func (p *Proxy) Call(w http.ResponseWriter, r *http.Request, key *APIKey) {
	start := time.Now()

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body", "invalid_request_error")
		return
	}
	var req chatReq
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeErr(w, http.StatusBadRequest, `body must be JSON with a "model" field`, "invalid_request_error")
		return
	}

	model := p.cfg.FindModel(req.Model)
	if model == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("model %q not found — see /v1/models", req.Model), "invalid_request_error")
		return
	}
	provider := p.cfg.ProviderByName(model.Provider)
	if provider == nil {
		writeErr(w, http.StatusInternalServerError, "model provider missing from config", "server_error")
		return
	}

	if key.MonthlyLimit > 0 {
		est := estCost(model, promptTokens(req.Messages), 0)
		if p.usage.MonthSpend(key.ID)+est > key.MonthlyLimit {
			writeErr(w, http.StatusPaymentRequired,
				fmt.Sprintf("monthly limit of %s reached for key %q", moneyStr(key.MonthlyLimit), key.Name),
				"insufficient_credits")
			return
		}
	}

	if provider.Name == "demo" {
		p.demoCall(w, body, &req, model, key, start)
		return
	}
	if !provider.HasKey() {
		writeErr(w, http.StatusServiceUnavailable, provider.MissingKeyMsg(), "provider_not_configured")
		return
	}

	// rewrite only the model field, preserving every other option
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error")
		return
	}
	raw["model"], _ = json.Marshal(model.Upstream)
	out, _ := json.Marshal(raw)

	upCtx, upCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer upCancel()
	up, err := http.NewRequestWithContext(upCtx, http.MethodPost, provider.BaseURL+provider.ChatPath, bytes.NewReader(out))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to build upstream request", "server_error")
		return
	}
	up.Header.Set("Content-Type", "application/json")
	if ak := provider.APIKey(); ak != "" {
		up.Header.Set("Authorization", "Bearer "+ak)
	}
	if provider.Name == "openrouter" {
		up.Header.Set("HTTP-Referer", "http://localhost:"+fmt.Sprint(p.cfg.Server.Port))
		up.Header.Set("X-Title", p.cfg.Server.Name)
	}

	log.Printf("-> %s via %s", model.ID, provider.Name)
	resp, err := p.http.Do(up)
	if err != nil {
		p.record(model, provider.Name, key, start, 502, 0, 0, req.Stream, "upstream unreachable: "+err.Error())
		writeErr(w, http.StatusBadGateway, "upstream provider unreachable: "+err.Error(), "upstream_error")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg := readUpstreamErr(resp)
		p.record(model, provider.Name, key, start, resp.StatusCode, 0, 0, req.Stream, msg)
		writeErr(w, resp.StatusCode, msg, "upstream_error")
		return
	}

	if req.Stream {
		p.relayStream(w, resp, &req, model, provider.Name, key, start)
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		p.record(model, provider.Name, key, start, 502, 0, 0, false, "upstream read error")
		writeErr(w, http.StatusBadGateway, "upstream read error", "upstream_error")
		return
	}

	var ur upResp
	_ = json.Unmarshal(data, &ur)
	prompt := promptTokens(req.Messages)
	compl := 0
	if ur.Usage != nil && ur.Usage.CompletionTokens > 0 {
		compl = ur.Usage.CompletionTokens
	} else if len(ur.Choices) > 0 {
		compl = utf8.RuneCountInString(ur.Choices[0].Message.Content) / 4
	}
	p.record(model, provider.Name, key, start, 200, prompt, compl, false, "")

	// normalize the model id, pass everything else through untouched
	var pass map[string]json.RawMessage
	_ = json.Unmarshal(data, &pass)
	pass["model"], _ = json.Marshal(model.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_ = json.NewEncoder(w).Encode(pass)
}

func readUpstreamErr(resp *http.Response) string {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	return fmt.Sprintf("upstream returned status %d", resp.StatusCode)
}

func promptTokens(msgs []chatMsg) int {
	n := 0
	for _, m := range msgs {
		n += utf8.RuneCountInString(m.Content)
	}
	return n / 4
}

func estCost(m *ModelConfig, prompt, compl int) float64 {
	return float64(prompt)/1e6*m.PriceIn + float64(compl)/1e6*m.PriceOut
}

func (p *Proxy) record(model *ModelConfig, provider string, key *APIKey, start time.Time, status, prompt, compl int, stream bool, errMsg string) {
	if key.ID != "local" {
		go p.keys.MarkUsed(key.ID)
	}
	p.usage.Record(UsageEntry{
		Time:      time.Now(),
		Model:     model.ID,
		Provider:  provider,
		KeyID:     key.ID,
		KeyName:   key.Name,
		PromptTok: prompt,
		ComplTok:  compl,
		CostUSD:   estCost(model, prompt, compl),
		LatencyMs: time.Since(start).Milliseconds(),
		Status:    status,
		Stream:    stream,
		Error:     errMsg,
	})
}

// relayStream pipes SSE chunks to the client as they arrive, counting
// generated characters so usage can be recorded when the stream ends.
func (p *Proxy) relayStream(w http.ResponseWriter, resp *http.Response, req *chatReq, model *ModelConfig, providerName string, key *APIKey, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported", "server_error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var complChars int
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(line[5:])
			if payload != "" && payload != "[DONE]" {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					for _, c := range chunk.Choices {
						complChars += len(c.Delta.Content)
					}
				}
			}
		}
		io.WriteString(w, line+"\n\n")
		flusher.Flush()
	}
	p.record(model, providerName, key, start, 200, promptTokens(req.Messages), complChars/4, true, "")
}

// demoCall serves the built-in "demo" provider so the product works with
// zero configuration: no keys, no upstreams, just install and go.
func (p *Proxy) demoCall(w http.ResponseWriter, body []byte, req *chatReq, model *ModelConfig, key *APIKey, start time.Time) {
	last := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	reply := fmt.Sprintf(
		"This is the built-in demo model of %s — no API key needed. "+
			"You asked: %q. Add a provider to config.yaml and set its key in .env to route real models.",
		p.cfg.Server.Name, truncate(last, 200))

	compl := utf8.RuneCountInString(reply) / 4
	if req.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported", "server_error")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		id := "demo-" + fmt.Sprint(time.Now().UnixNano())
		words := strings.SplitAfter(reply, " ")
		for i, word := range words {
			chunk := map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"model":   model.ID,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": word}}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			if i < len(words)-1 {
				time.Sleep(25 * time.Millisecond)
			}
		}
		done := map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": model.ID,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}
		b, _ := json.Marshal(done)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	} else {
		resp := map[string]any{
			"id":     "demo-" + fmt.Sprint(time.Now().UnixNano()),
			"object": "chat.completion",
			"model":  model.ID,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": Usage{PromptTokens: promptTokens(req.Messages), CompletionTokens: compl, TotalTokens: promptTokens(req.Messages) + compl},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
	p.record(model, "demo", key, start, 200, promptTokens(req.Messages), compl, req.Stream, "")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// writeErr writes an OpenAI-style error response.
func writeErr(w http.ResponseWriter, status int, msg, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": status},
	})
}

func moneyStr(v float64) string {
	if v >= 0.01 {
		return fmt.Sprintf("$%.2f", v)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("$%.6f", v), "0"), ".")
}
