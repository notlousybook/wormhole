package wormhole

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// UsageEntry is one completed (or failed) request through the router.
type UsageEntry struct {
	Time       time.Time `json:"time"`
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	KeyID      string    `json:"key_id"`
	KeyName    string    `json:"key_name"`
	PromptTok  int       `json:"prompt_tokens"`
	ComplTok   int       `json:"completion_tokens"`
	CostUSD    float64   `json:"cost_usd"`
	LatencyMs  int64     `json:"latency_ms"`
	Status     int       `json:"status"`
	Stream     bool      `json:"stream"`
	Error      string    `json:"error,omitempty"`
}

// UsageStore appends entries to monthly JSONL files under data/ and keeps
// per-key month-to-date spend in memory for limit enforcement.
type UsageStore struct {
	mu         sync.Mutex
	dir        string
	monthSpend map[string]float64 // keyID -> USD spent this calendar month
}

func NewUsageStore(dataDir string) (*UsageStore, error) {
	dataDir = resolveDataDir(dataDir)
	u := &UsageStore{dir: dataDir, monthSpend: map[string]float64{}}
	if err := u.rebuildMonth(); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *UsageStore) fileFor(t time.Time) string {
	return filepath.Join(u.dir, "usage-"+t.Format("2006-01")+".jsonl")
}

func (u *UsageStore) rebuildMonth() error {
	f, err := os.Open(u.fileFor(time.Now()))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e UsageEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			u.monthSpend[e.KeyID] += e.CostUSD
		}
	}
	return sc.Err()
}

func (u *UsageStore) Record(e UsageEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	f, err := os.OpenFile(u.fileFor(e.Time), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
	if e.CostUSD > 0 {
		u.monthSpend[e.KeyID] += e.CostUSD
	}
}

func (u *UsageStore) MonthSpend(keyID string) float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.monthSpend[keyID]
}

// Range returns entries from the last `days` days, oldest first.
func (u *UsageStore) Range(days int) []UsageEntry {
	now := time.Now()
	var files []string
	seen := map[string]bool{}
	for i := 0; i <= days; i++ {
		t := now.AddDate(0, 0, -i)
		f := u.fileFor(t)
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	var out []UsageEntry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var e UsageEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// Totals aggregates a set of entries.
type Totals struct {
	Requests   int     `json:"requests"`
	Errors     int     `json:"errors"`
	PromptTok  int     `json:"prompt_tokens"`
	ComplTok   int     `json:"completion_tokens"`
	CostUSD    float64 `json:"cost_usd"`
	AvgLatency int64   `json:"avg_latency_ms"`
}

func SumEntries(entries []UsageEntry) Totals {
	var t Totals
	var latSum int64
	latN := 0
	for _, e := range entries {
		t.Requests++
		if e.Status >= 400 {
			t.Errors++
		}
		t.PromptTok += e.PromptTok
		t.ComplTok += e.ComplTok
		t.CostUSD += e.CostUSD
		if e.LatencyMs > 0 {
			latSum += e.LatencyMs
			latN++
		}
	}
	if latN > 0 {
		t.AvgLatency = latSum / int64(latN)
	}
	return t
}

// DailyBucket is one day's totals, for charts.
type DailyBucket struct {
	Date        string  `json:"date"`
	Requests    int     `json:"requests"`
	Tokens      int     `json:"tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

func DailyBuckets(entries []UsageEntry, days int) []DailyBucket {
	byDate := map[string]*DailyBucket{}
	var order []string
	today := time.Now()
	for i := days - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -i).Format("2006-01-02")
		byDate[d] = &DailyBucket{Date: d}
		order = append(order, d)
	}
	for _, e := range entries {
		d := e.Time.Format("2006-01-02")
		if b, ok := byDate[d]; ok {
			b.Requests++
			b.Tokens += e.PromptTok + e.ComplTok
			b.CostUSD += e.CostUSD
		}
	}
	out := make([]DailyBucket, 0, len(order))
	for _, d := range order {
		out = append(out, *byDate[d])
	}
	return out
}

// ModelTotals aggregates entries per model.
type ModelTotals struct {
	Model      string  `json:"model"`
	Requests   int     `json:"requests"`
	Tokens     int     `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	AvgLatency int64   `json:"avg_latency_ms"`
	Errors     int     `json:"errors"`
}

func PerModel(entries []UsageEntry) []ModelTotals {
	byModel := map[string]*ModelTotals{}
	for _, e := range entries {
		m, ok := byModel[e.Model]
		if !ok {
			m = &ModelTotals{Model: e.Model}
			byModel[e.Model] = m
		}
		m.Requests++
		m.Tokens += e.PromptTok + e.ComplTok
		m.CostUSD += e.CostUSD
		if e.Status >= 400 {
			m.Errors++
		}
		if e.LatencyMs > 0 {
			// running average
			m.AvgLatency = (m.AvgLatency*int64(m.Requests-1) + e.LatencyMs) / int64(m.Requests)
		}
	}
	out := make([]ModelTotals, 0, len(byModel))
	for _, m := range byModel {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requests > out[j].Requests })
	return out
}
