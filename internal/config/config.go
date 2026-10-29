package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	DownloaderSocket   string
	ArchiveEventSocket string
	AuthDB             string
	SessionTTL         time.Duration
	AccessDB           string
	AccessMasterKey    string
	CentralSocket      string
}

func Load() Config {
	home, _ := os.UserHomeDir()
	runtime := runtimeRoot(home)
	return Config{
		DownloaderSocket: expandHomeToken(env("SATELLITE_DOWNLOADER_SOCKET",
			filepath.Join(runtime, "satellite-data-downloader", "downloader.sock")), home),
		ArchiveEventSocket: expandHomeToken(env("SATELLITE_ARCHIVE_EVENT_SOCKET",
			filepath.Join(runtime, "satellite-data", "archive-events.sock")), home),
		AuthDB:          expandHomeToken(env("SATELLITE_AUTH_DB", filepath.Join(home, ".local", "state", "satellite-data", "auth.sqlite3")), home),
		SessionTTL:      12 * time.Hour,
		AccessDB:        expandHomeToken(env("SATELLITE_ACCESS_DB", filepath.Join(home, ".local", "state", "satellite-data", "access.sqlite3")), home),
		AccessMasterKey: expandHomeToken(env("SATELLITE_ACCESS_MASTER_KEY", filepath.Join(home, ".config", "satellite-data", "master.key")), home),
		CentralSocket:   expandHomeToken(env("SATELLITE_CENTRAL_SOCKET", filepath.Join(runtime, "satellite-data", "central.sock")), home),
	}
}

func expandHomeToken(value, home string) string {
	if home != "" && strings.HasPrefix(value, "%h/") {
		return filepath.Join(home, strings.TrimPrefix(value, "%h/"))
	}
	return value
}

func runtimeRoot(home string) string {
	if value := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); value != "" {
		return value
	}
	return filepath.Join(home, ".local", "state")
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
