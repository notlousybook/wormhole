package main

// Mock OpenAI-compatible upstream for testing Look!ReRouter end to end
// without any real API keys. Run on port 18080:
//
//	go run ./dev/mock
//
// Then point a provider in a test config at http://127.0.0.1:18080/v1.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type req struct {
	Model    string `json:"model"`
	Messages []msg  `json:"messages"`
	Stream   bool   `json:"stream"`
}

func main() {
	http.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var q req
		_ = json.NewDecoder(r.Body).Decode(&q)
		reply := fmt.Sprintf("[mock %s] You said: %s", q.Model, lastUser(q.Messages))

		if !q.Stream {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": "mock-1", "object": "chat.completion", "model": q.Model,
				"choices": []any{map[string]any{
					"index": 0, "message": map[string]any{"role": "assistant", "content": reply},
					"finish_reason": "stop",
				}},
				"usage": map[string]int{
					"prompt_tokens":     10,
					"completion_tokens": 20,
					"total_tokens":      30,
				},
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		words := strings.SplitAfter(reply, " ")
		for i, word := range words {
			chunk := map[string]any{
				"id": "mock-1", "object": "chat.completion.chunk", "model": q.Model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": word}}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			f.Flush()
			if i < len(words)-1 {
				time.Sleep(15 * time.Millisecond)
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	})

	http.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": "mock-model", "object": "model"}},
		})
	})

	fmt.Println("mock upstream on http://127.0.0.1:18080/v1")
	http.ListenAndServe("127.0.0.1:18080", nil)
}

func lastUser(ms []msg) string {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role == "user" {
			return ms[i].Content
		}
	}
	return "(none)"
}
