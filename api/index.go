// Vercel serverless entry point: every request (pages, /v1/*, /api/*)
// is handled by one Go function. main() never runs on Vercel; Handler does.
//
// State (keys, usage) lives in the ephemeral /tmp filesystem, so keys
// created in the dashboard last until the next cold start. Provider API
// keys come from Vercel environment variables (GROQ_API_KEY, ...), exactly
// like .env locally. Keyless models work with zero configuration.
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/wormhole/wormhole/internal/wormhole"
)

var (
	srv     *wormhole.Server
	once    sync.Once
	initErr error
)

func getServer() (*wormhole.Server, error) {
	once.Do(func() {
		cfg, err := wormhole.LoadDefaultConfig()
		if err != nil {
			initErr = err
			return
		}
		dataDir := filepath.Join(os.TempDir(), "wormhole-data")
		keys, err := wormhole.NewKeyStore(dataDir)
		if err != nil {
			initErr = err
			return
		}
		usage, err := wormhole.NewUsageStore(dataDir)
		if err != nil {
			initErr = err
			return
		}
		srv = wormhole.NewServer(cfg, keys, usage)
	})
	return srv, initErr
}

func Handler(w http.ResponseWriter, r *http.Request) {
	s, err := getServer()
	if err != nil {
		http.Error(w, "server init failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Handler().ServeHTTP(w, r)
}

func main() {}
