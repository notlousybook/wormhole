package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/wormhole/wormhole/internal/wormhole"
)

func main() {
	// On serverless platforms there is no config file and no listener:
	// api/index.go's Handler serves everything from the embedded config.
	// This binary is for local/self-hosted use.
	if wormhole.IsServerless() {
		return
	}

	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	n, err := wormhole.LoadEnv(".env")
	if err != nil {
		log.Fatalf("could not read .env: %v", err)
	}
	if n > 0 {
		fmt.Printf("→ loaded %d variable(s) from .env\n", n)
	}

	cfg, err := wormhole.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	keys, err := wormhole.NewKeyStore("data")
	if err != nil {
		log.Fatalf("keys: %v", err)
	}
	usage, err := wormhole.NewUsageStore("data")
	if err != nil {
		log.Fatalf("usage: %v", err)
	}

	srv := wormhole.NewServer(cfg, keys, usage)
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port)
	fmt.Printf("\n  %s is running\n", cfg.Server.Name)
	fmt.Printf("  dashboard  → http://localhost:%d\n", cfg.Server.Port)
	fmt.Printf("  api        → http://localhost:%d/v1\n", cfg.Server.Port)
	fmt.Printf("  models: %d   providers: %d   keys: %d\n\n", len(cfg.Models), len(cfg.Providers), keys.Count())
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
