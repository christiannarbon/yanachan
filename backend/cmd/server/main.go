// Command server runs the Yana-chan 4K backend.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/api"
	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
	"github.com/christiannarbon/yanachan-4k/backend/internal/webui"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			log.Printf("config: %s", line)
		}
		os.Exit(1)
	}
	log.Printf("auth mode: %s", cfg.AuthMode)
	if cfg.AuthMode == config.AuthServer && !cfg.Secure() && !isLoopback(cfg.PublicHost()) {
		log.Printf("warning: GHDASH_PUBLIC_URL is plain http on a non-loopback host; session cookies will cross the network unencrypted")
	}
	if cfg.AuthMode == config.AuthServer {
		// P2-11 replaces this with the server-mode wiring.
		log.Printf("config: server mode is not wired up yet")
		os.Exit(1)
	}

	store, err := state.New(cfg.SettingsPath(), cfg.SessionPath(), state.DefaultSettings(cfg.DefaultLimit))
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(cfg, store, webui.Handler()).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("Yana-chan 4K listening on http://%s", cfg.Addr)
	log.Printf("state directory: %s", cfg.StateDir)
	if cfg.ClientID == "" {
		log.Printf("GITHUB_CLIENT_ID is unset: OAuth device sign-in is disabled")
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
