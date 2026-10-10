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
	var cfg *wormhole.Config
	var err error

	if wormhole.IsServerless() {
		// Vercel runs this binary as a long-running server and proxies
		// HTTP to $PORT. Config comes from the embedded free config;
		// provider keys come from environment variables.
		cfg, err = wormhole.LoadDefaultConfig()
		if err != nil {
			log.Fatalf("config error: %v", err)
		}
	} else {
		configPath := flag.String("config", "config.yaml", "path to config file")
		flag.Parse()

		n, err := wormhole.LoadEnv(".env")
		if err != nil {
			log.Fatalf("could not read .env: %v", err)
		}
		if n > 0 {
			fmt.Printf("→ loaded %d variable(s) from .env\n", n)
		}

		cfg, err = wormhole.LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("config error: %v", err)
		}
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
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	fmt.Printf("\n  %s is running on %s\n", cfg.Server.Name, addr)
	fmt.Printf("  models: %d   providers: %d   keys: %d\n\n", len(cfg.Models), len(cfg.Providers), keys.Count())
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
