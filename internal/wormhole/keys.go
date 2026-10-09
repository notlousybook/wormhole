package wormhole

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// APIKey is a dashboard-managed key that authorizes callers of /v1/*.
// Only a SHA-256 hash is stored; the full key is shown once at creation.
type APIKey struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Prefix       string    `json:"prefix"` // display form, e.g. sk-wormhole-a1b2…9f
	Hash         string    `json:"hash"`
	Created      time.Time `json:"created"`
	LastUsed     time.Time `json:"last_used"`
	MonthlyLimit float64   `json:"monthly_limit"` // USD per calendar month, 0 = unlimited
	Disabled     bool      `json:"disabled"`
}

func (k *APIKey) Public() map[string]any {
	return map[string]any{
		"id":            k.ID,
		"name":          k.Name,
		"prefix":        k.Prefix,
		"created":       k.Created,
		"last_used":     k.LastUsed,
		"monthly_limit": k.MonthlyLimit,
		"disabled":      k.Disabled,
	}
}

// KeyStore persists keys in data/keys.json.
type KeyStore struct {
	mu     sync.RWMutex
	path   string
	keys   []*APIKey
	byHash map[string]*APIKey
}

func NewKeyStore(dataDir string) (*KeyStore, error) {
	dataDir = resolveDataDir(dataDir)
	ks := &KeyStore{
		path:   filepath.Join(dataDir, "keys.json"),
		byHash: map[string]*APIKey{},
	}
	if data, err := os.ReadFile(ks.path); err == nil {
		if err := json.Unmarshal(data, &ks.keys); err != nil {
			return nil, fmt.Errorf("parse keys.json: %w", err)
		}
	}
	ks.reindex()
	return ks, nil
}

func (ks *KeyStore) reindex() {
	ks.byHash = make(map[string]*APIKey, len(ks.keys))
	for _, k := range ks.keys {
		ks.byHash[k.Hash] = k
	}
}

func (ks *KeyStore) save() error {
	data, err := json.MarshalIndent(ks.keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := ks.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ks.path)
}

// Create generates a new key and returns the plaintext (shown once) plus the record.
func (ks *KeyStore) Create(name string, monthlyLimit float64) (string, *APIKey, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	full := "sk-wormhole-" + hex.EncodeToString(raw)
	rec := &APIKey{
		ID:           full[len("sk-wormhole-"):][:8],
		Name:         name,
		Prefix:       full[:len("sk-wormhole-")+4] + "…" + full[len(full)-4:],
		Hash:         hashKey(full),
		Created:      time.Now(),
		MonthlyLimit: monthlyLimit,
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.keys = append(ks.keys, rec)
	ks.reindex()
	if err := ks.save(); err != nil {
		return "", nil, err
	}
	return full, rec, nil
}

// Verify returns the key record for a plaintext key.
func (ks *KeyStore) Verify(full string) (*APIKey, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	rec, ok := ks.byHash[hashKey(full)]
	if !ok || rec.Disabled {
		return nil, false
	}
	return rec, true
}

func (ks *KeyStore) Delete(id string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for i, k := range ks.keys {
		if k.ID == id {
			ks.keys = append(ks.keys[:i], ks.keys[i+1:]...)
			ks.reindex()
			if err := ks.save(); err != nil {
				return false
			}
			return true
		}
	}
	return false
}

func (ks *KeyStore) MarkUsed(id string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for _, k := range ks.keys {
		if k.ID == id {
			k.LastUsed = time.Now()
			_ = ks.save()
			return
		}
	}
}

func (ks *KeyStore) List() []*APIKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]*APIKey, len(ks.keys))
	copy(out, ks.keys)
	return out
}

func (ks *KeyStore) Count() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return len(ks.keys)
}

func hashKey(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}
