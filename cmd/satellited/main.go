package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/archiveevent"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/config"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadcoord"
	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadmon"
	"github.com/nuist-fy-lab/satellite-data/internal/httpapi"
	"github.com/nuist-fy-lab/satellite-data/internal/indexer"
	"github.com/nuist-fy-lab/satellite-data/internal/linkmon"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func main() {
	runtimeConfig := config.Load()
	addr := flag.String("addr", envOr("SATELLITED_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	stateDB := flag.String("state-db", envOr("SATELLITED_STATE_DB", defaultStateDB()), "persistent state database")
	initializeStateOnly := flag.Bool("init-state-only", false, "initialize the selected state database, then exit without starting services")
	archiveRoot := flag.String("archive", envOr("SATELLITED_ARCHIVE", defaultArchiveRoot()), "local satellite archive root")
	migrateAccess := flag.Bool("migrate-access", false, "import legacy downloader access into an empty central vault and exit")
	legacyDB := flag.String("legacy-access-db", "", "stopped downloader database path (migration only)")
	legacyKey := flag.String("legacy-access-key", "", "legacy private master key path (migration only)")
	requireHost := flag.String("require-host", strings.TrimSpace(os.Getenv("SATELLITE_REQUIRE_HOST")), "refuse to start unless hostname matches")
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("unexpected positional arguments")
		os.Exit(2)
	}
	if err := enforceRequiredHost(*requireHost); err != nil {
		slog.Error("host restriction check failed", "error", err)
		os.Exit(1)
	}
	if *initializeStateOnly {
		store, err := state.Open(*stateDB)
		if err != nil {
			slog.Error("initialize state database", "error", err)
			os.Exit(1)
		}
		if err := store.Close(); err != nil {
			slog.Error("close initialized state database", "error", err)
			os.Exit(1)
		}
		slog.Info("persistent state schema initialized")
		return
	}
	centralVault, err := central.OpenVault(runtimeConfig.AccessMasterKey)
	if err != nil {
		slog.Error("open central credential vault", "error", err)
		os.Exit(1)
	}
	centralStore, err := central.OpenStore(runtimeConfig.AccessDB, centralVault)
	if err != nil {
		slog.Error("open central access store", "error", err)
		os.Exit(1)
	}
	defer centralStore.Close()
	if *migrateAccess {
		if *legacyDB == "" || *legacyKey == "" {
			slog.Error("migration requires legacy-access-db and legacy-access-key paths")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		counts, err := centralStore.ImportLegacy(ctx, *legacyDB, *legacyKey)
		if err != nil {
			slog.Error("access migration failed; destination unchanged", "error", err)
			os.Exit(1)
		}
		slog.Info("access migration committed", "credentials", counts.Credentials, "proxies", counts.Proxies, "accounts", counts.Accounts)
		return
	}

	registry, err := app.NewRegistryWithCloudSatMetadata(central.NewCloudSatMetadataClient(runtimeConfig.CentralSocket))
	if err != nil {
		slog.Error("initialize providers", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*stateDB), 0o750); err != nil {
		slog.Error("create state directory", "error", err)
		os.Exit(1)
	}
	if err := checkLegacyStateDB(*stateDB); err != nil {
		slog.Error("open persistent state", "error", err)
		os.Exit(1)
	}
	store, err := state.Open(*stateDB)
	if err != nil {
		slog.Error("open persistent state", "path", *stateDB, "error", err)
		os.Exit(1)
	}
	// Do not block process shutdown on sql.DB.Close while background provider
	// requests are unwinding. The state database runs in WAL mode and the OS
	// closes its file descriptors when the daemon exits; SQLite recovers any
	// interrupted transaction on the next open.
	if err := store.RecoverOfficialWork(context.Background()); err != nil {
		slog.Error("recover official index queue", "error", err)
		os.Exit(1)
	}
	if err := store.RecoverDownloadRequests(context.Background()); err != nil {
		slog.Error("recover download requests", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	linkmon.New(store).Run(ctx)
	archiveIndexer := indexer.New(registry, store, *archiveRoot,
		os.Getenv("SATELLITED_EARTHCARE_ARCHIVE"), os.Getenv("SATELLITED_ERA5_ARCHIVE"), os.Getenv("SATELLITED_HIMAWARI_ARCHIVE"))
	archiveIndexer.Run(ctx)
	downloaderClient := downloader.New(runtimeConfig.DownloaderSocket)
	downloadmon.New(store, downloaderClient).Run(ctx)
	authStore, err := authstore.Open(runtimeConfig.AuthDB)
	if err != nil {
		slog.Error("open access auth store", "error", err)
		os.Exit(1)
	}
	defer authStore.Close()
	centralService := central.NewService(centralStore)
	go func() {
		if err := central.RunGateway(ctx, runtimeConfig.CentralSocket, centralService); err != nil && ctx.Err() == nil {
			slog.Error("central access gateway stopped", "error", err)
			stop()
		}
	}()
	coordinator, err := downloadcoord.New(registry, store, downloaderClient, centralService)
	if err != nil {
		slog.Error("initialize download coordinator", "error", err)
		os.Exit(1)
	}
	coordinator.Run(ctx)
	go func() {
		if err := archiveevent.Run(ctx, runtimeConfig.ArchiveEventSocket, archiveIndexer.RecordArchiveEvent); err != nil && ctx.Err() == nil {
			slog.Error("archive event listener stopped", "error", err)
			stop()
		}
	}()

	server := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewWithAuth(registry, store, downloaderClient, authStore, runtimeConfig.SessionTTL, centralService),
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
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if strings.HasPrefix(value, "%h/") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				return filepath.Join(home, strings.TrimPrefix(value, "%h/"))
			}
		}
		return value
	}
	return fallback
}

func enforceRequiredHost(required string) error {
	required = strings.ToLower(strings.TrimSpace(required))
	if required == "" {
		return nil
	}
	actual, err := os.Hostname()
	if err != nil {
		return err
	}
	actual = strings.ToLower(strings.TrimSpace(actual))
	if actual == required || strings.HasPrefix(actual, required+".") {
		return nil
	}
	return fmt.Errorf("running on host %q, restricted to %q", actual, required)
}

func defaultStateDB() string {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		return filepath.Join(root, "satellite-data", "state.sqlite3")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "satellite-data.sqlite3"
	}
	return filepath.Join(home, ".local", "state", "satellite-data", "state.sqlite3")
}

func checkLegacyStateDB(path string) error {
	if filepath.Base(path) != "state.sqlite3" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacy := filepath.Join(filepath.Dir(path), "state.db")
	if _, err := os.Stat(legacy); err == nil {
		return fmt.Errorf("legacy state database %s exists; stop satellited and migrate it to %s before starting", legacy, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
