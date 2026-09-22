package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/httpapi"
	"github.com/nuist-fy-lab/satellite-data/internal/indexer"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func main() {
	addr := flag.String("addr", envOr("SATELLITED_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	stateDB := flag.String("state-db", envOr("SATELLITED_STATE_DB", defaultStateDB()), "persistent state database")
	archiveRoot := flag.String("archive", envOr("SATELLITED_ARCHIVE", defaultArchiveRoot()), "local satellite archive root")
	flag.Parse()

	registry, err := app.NewRegistry()
	if err != nil {
		slog.Error("initialize providers", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*stateDB), 0o750); err != nil {
		slog.Error("create state directory", "error", err)
		os.Exit(1)
	}
	store, err := state.Open(*stateDB)
	if err != nil {
		slog.Error("open persistent state", "path", *stateDB, "error", err)
		os.Exit(1)
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	indexer.New(registry, store, *archiveRoot).Run(ctx)

	server := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(registry, store),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("graceful shutdown", "error", err)
		}
	}()
	slog.Info("satellited listening", "addr", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func defaultStateDB() string {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		return filepath.Join(root, "satellite-data", "state.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "satellite-data.db"
	}
	return filepath.Join(home, ".local", "state", "satellite-data", "state.db")
}

func defaultArchiveRoot() string {
	if root := os.Getenv("XDG_DATA_HOME"); root != "" {
		return filepath.Join(root, "satellite-data", "archive")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "archive"
	}
	return filepath.Join(home, ".local", "share", "satellite-data", "archive")
}
