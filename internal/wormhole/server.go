package wormhole

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed all:web
var webFS embed.FS

type Server struct {
	cfg   *Config
	proxy *Proxy
	keys  *KeyStore
	usage *UsageStore
	mux   *http.ServeMux
	start time.Time
	anon  *anonPool
}

// anonPool is the keyless shared pool: anonymous requests are allowed but
// rate-limited per IP, since keyless providers rate-limit per IP too.
type anonPool struct {
	mu       sync.Mutex
	buckets  map[string]*anonBucket
	perMinute int
}

type anonBucket struct {
	window time.Time
	count  int
}

func newAnonPool(perMinute int) *anonPool {
	return &anonPool{buckets: map[string]*anonBucket{}, perMinute: perMinute}
}

func (a *anonPool) Allow(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	// sweep stale buckets
	for k, b := range a.buckets {
		if now.Sub(b.window) > time.Minute {
			delete(a.buckets, k)
		}
	}
	b, ok := a.buckets[ip]
	if !ok || now.Sub(b.window) >= time.Minute {
		a.buckets[ip] = &anonBucket{window: now, count: 1}
		return true
	}
	if b.count >= a.perMinute {
		return false
	}
	b.count++
	return true
}

func NewServer(cfg *Config, keys *KeyStore, usage *UsageStore) *Server {
	s := &Server{
		cfg:   cfg,
		proxy: NewProxy(cfg, keys, usage),
		keys:  keys,
		usage: usage,
		mux:   http.NewServeMux(),
		start: time.Now(),
		anon:  newAnonPool(30), // 30 anonymous requests/min per IP
	}
	s.routes()
	return s
}

// Handler exposes the full router (pages, /v1/*, /api/*) as an http.Handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	// pages
	s.mux.HandleFunc("GET /{$}", s.page("web/index.html"))
	s.mux.HandleFunc("GET /model/{id...}", s.page("web/model.html"))
	s.mux.HandleFunc("GET /playground", s.page("web/playground.html"))
	s.mux.HandleFunc("GET /keys", s.page("web/keys.html"))
	s.mux.HandleFunc("GET /usage", s.page("web/usage.html"))
	s.mux.HandleFunc("GET /docs", s.page("web/docs.html"))

	// static assets
	assets, _ := fs.Sub(webFS, "web")
	s.mux.Handle("GET /css/", http.FileServer(http.FS(assets)))
	s.mux.Handle("GET /js/", http.FileServer(http.FS(assets)))
	s.mux.Handle("GET /fonts/", http.FileServer(http.FS(assets)))
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// OpenAI-compatible API (CORS enabled so web apps can call it)
	s.mux.HandleFunc("GET /v1/models", s.cors(s.listModels))
	s.mux.HandleFunc("POST /v1/chat/completions", s.cors(s.chatCompletions))
	s.mux.HandleFunc("OPTIONS /v1/", s.corsPreflight)

	// management API (dashboard, localhost only)
	s.mux.HandleFunc("GET /api/overview", s.localOnly(s.overview))
	s.mux.HandleFunc("GET /api/models", s.localOnly(s.apiModels))
	s.mux.HandleFunc("GET /api/keys", s.localOnly(s.listKeys))
	s.mux.HandleFunc("POST /api/keys", s.localOnly(s.createKey))
	s.mux.HandleFunc("DELETE /api/keys/{id}", s.localOnly(s.deleteKey))
	s.mux.HandleFunc("GET /api/usage", s.localOnly(s.apiUsage))
	s.mux.HandleFunc("GET /api/usage/recent", s.localOnly(s.apiRecent))

	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
}

// ---------- pages ----------

func (s *Server) page(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := webFS.ReadFile(file)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

// ---------- OpenAI-compatible API ----------

func (s *Server) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next(w, r)
	}
}

func (s *Server) corsPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	type modelOut struct {
		ID      string             `json:"id"`
		Object  string             `json:"object"`
		Created int64              `json:"created"`
		OwnedBy string             `json:"owned_by"`
		Name    string             `json:"name"`
		Tags    []string           `json:"tags,omitempty"`
		Context int                `json:"context_length,omitempty"`
		Pricing map[string]float64 `json:"pricing"`
	}
	created := s.start.Unix()
	out := make([]modelOut, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		out = append(out, modelOut{
			ID: m.ID, Object: "model", Created: created, OwnedBy: m.Provider,
			Name: m.Name, Tags: m.Tags, Context: m.Context,
			Pricing: map[string]float64{"prompt": m.PriceIn, "completion": m.PriceOut},
		})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": out})
}

// resolveKey authorizes a /v1/chat/completions request:
//   - Bearer token matching a dashboard key → that key
//   - no token from localhost → "local" (dashboard, curl on this machine)
//   - no token from anywhere else → "keyless pool", per-IP rate limited
func (s *Server) resolveKey(r *http.Request) (*APIKey, bool) {
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		token := strings.TrimSpace(after)
		if token != "" {
			if key, valid := s.keys.Verify(token); valid {
				return key, true
			}
			return nil, false
		}
	}
	if isLoopback(r) {
		return &APIKey{ID: "local", Name: "local dashboard"}, true
	}
	ip := clientIP(r)
	if s.anon.Allow(ip) {
		return &APIKey{ID: "pool", Name: "keyless pool"}, true
	}
	return nil, false
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.Index(fwd, ","); i >= 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	key, ok := s.resolveKey(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid API key, or the anonymous pool rate limit is exhausted — retry shortly or create a key on the /keys page", "authentication_error")
		return
	}
	s.proxy.Call(w, r, key)
}

// ---------- management API ----------

func (s *Server) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeErr(w, http.StatusForbidden, "management API is only available from localhost", "authentication_error")
			return
		}
		next(w, r)
	}
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	entries := s.usage.Range(1)
	today := SumEntries(entries)
	all := s.usage.Range(7)
	week := SumEntries(all)

	providers := []map[string]any{}
	for _, p := range s.cfg.Providers {
		models := 0
		for _, m := range s.cfg.Models {
			if m.Provider == p.Name {
				models++
			}
		}
		providers = append(providers, map[string]any{
			"name": p.Name, "base_url": p.BaseURL, "has_key": p.HasKey(),
			"key_env": p.KeyEnv, "models": models,
		})
	}
	writeJSON(w, 200, map[string]any{
		"name":          s.cfg.Server.Name,
		"port":          s.cfg.Server.Port,
		"uptime":        time.Since(s.start).Round(time.Second).String(),
		"providers":     providers,
		"model_count":   len(s.cfg.Models),
		"key_count":     s.keys.Count(),
		"today":         today,
		"week":          week,
	})
}

func (s *Server) apiModels(w http.ResponseWriter, r *http.Request) {
	recent := s.usage.Range(7)
	type modelOut struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Provider    string   `json:"provider"`
		ProviderKey bool     `json:"provider_key_ok"`
		Tags        []string `json:"tags"`
		Context     int      `json:"context_length"`
		PriceIn     float64  `json:"price_in"`
		PriceOut    float64  `json:"price_out"`
		Free        bool     `json:"free"`
		Requests    int      `json:"requests_7d"`
		AvgLatency  int64    `json:"avg_latency_ms"`
	}
	perModel := map[string]ModelTotals{}
	for _, m := range PerModel(recent) {
		perModel[m.Model] = m
	}
	out := []modelOut{}
	for _, m := range s.cfg.Models {
		p := s.cfg.ProviderByName(m.Provider)
		t := perModel[m.ID]
		out = append(out, modelOut{
			ID: m.ID, Name: m.Name, Provider: m.Provider,
			ProviderKey: p.HasKey(), Tags: m.Tags, Context: m.Context,
			PriceIn: m.PriceIn, PriceOut: m.PriceOut, Free: m.IsFree(),
			Requests: t.Requests, AvgLatency: t.AvgLatency,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, k := range s.keys.List() {
		item := k.Public()
		item["month_spend"] = s.usage.MonthSpend(k.ID)
		out = append(out, item)
	}
	writeJSON(w, 200, out)
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string  `json:"name"`
		MonthlyLimit float64 `json:"monthly_limit"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "key name is required", "invalid_request_error")
		return
	}
	if body.MonthlyLimit == 0 {
		body.MonthlyLimit = s.cfg.Limits.DefaultKeyMonthlyLimit
	}
	full, rec, err := s.keys.Create(strings.TrimSpace(body.Name), body.MonthlyLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create key: "+err.Error(), "server_error")
		return
	}
	writeJSON(w, 201, map[string]any{"key": full, "record": rec.Public()})
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.keys.Delete(id) {
		writeErr(w, http.StatusNotFound, "key not found", "invalid_request_error")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) apiUsage(w http.ResponseWriter, r *http.Request) {
	days := 7
	entries := s.usage.Range(days)
	writeJSON(w, 200, map[string]any{
		"days":    days,
		"totals":  SumEntries(entries),
		"daily":   DailyBuckets(entries, days),
		"models":  PerModel(entries),
	})
}

func (s *Server) apiRecent(w http.ResponseWriter, r *http.Request) {
	limit := 50
	entries := s.usage.Range(30)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	// newest first
	out := make([]UsageEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i])
	}
	writeJSON(w, 200, out)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
