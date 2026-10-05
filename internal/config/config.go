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
	return Config{
		DownloaderSocket: env("SATELLITE_DOWNLOADER_SOCKET",
			filepath.Join(home, ".local", "state", "satellite-data-downloader", "downloader.sock")),
		ArchiveEventSocket: env("SATELLITE_ARCHIVE_EVENT_SOCKET",
			filepath.Join(home, ".local", "state", "satellite-data", "archive-events.sock")),
		AuthDB:          env("SATELLITE_AUTH_DB", filepath.Join(home, ".local", "state", "satellite-data", "auth.sqlite3")),
		SessionTTL:      12 * time.Hour,
		AccessDB:        env("SATELLITE_ACCESS_DB", filepath.Join(home, ".local", "state", "satellite-data", "access.sqlite3")),
		AccessMasterKey: env("SATELLITE_ACCESS_MASTER_KEY", filepath.Join(home, ".config", "satellite-data", "master.key")),
		CentralSocket:   env("SATELLITE_CENTRAL_SOCKET", filepath.Join(home, ".local", "state", "satellite-data", "central.sock")),
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
