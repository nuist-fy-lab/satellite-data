package config

import (
	"path/filepath"
	"testing"
)

func TestLoadUsesXDGRunTimeForIPC(t *testing.T) {
	home := t.TempDir()
	runtime := filepath.Join(t.TempDir(), "run")
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("SATELLITE_DOWNLOADER_SOCKET", "")
	t.Setenv("SATELLITE_ARCHIVE_EVENT_SOCKET", "")
	t.Setenv("SATELLITE_CENTRAL_SOCKET", "")

	cfg := Load()
	if want := filepath.Join(runtime, "satellite-data-downloader", "downloader.sock"); cfg.DownloaderSocket != want {
		t.Fatalf("DownloaderSocket=%q want %q", cfg.DownloaderSocket, want)
	}
	if want := filepath.Join(runtime, "satellite-data", "archive-events.sock"); cfg.ArchiveEventSocket != want {
		t.Fatalf("ArchiveEventSocket=%q want %q", cfg.ArchiveEventSocket, want)
	}
	if want := filepath.Join(runtime, "satellite-data", "central.sock"); cfg.CentralSocket != want {
		t.Fatalf("CentralSocket=%q want %q", cfg.CentralSocket, want)
	}
	if want := filepath.Join(home, ".local", "state", "satellite-data", "access.sqlite3"); cfg.AccessDB != want {
		t.Fatalf("AccessDB=%q want %q", cfg.AccessDB, want)
	}
}

func TestLoadFallsBackToHomeStateForIPC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("SATELLITE_DOWNLOADER_SOCKET", "")
	t.Setenv("SATELLITE_ARCHIVE_EVENT_SOCKET", "")
	t.Setenv("SATELLITE_CENTRAL_SOCKET", "")

	cfg := Load()
	root := filepath.Join(home, ".local", "state")
	if want := filepath.Join(root, "satellite-data-downloader", "downloader.sock"); cfg.DownloaderSocket != want {
		t.Fatalf("DownloaderSocket=%q want %q", cfg.DownloaderSocket, want)
	}
	if want := filepath.Join(root, "satellite-data", "archive-events.sock"); cfg.ArchiveEventSocket != want {
		t.Fatalf("ArchiveEventSocket=%q want %q", cfg.ArchiveEventSocket, want)
	}
	if want := filepath.Join(root, "satellite-data", "central.sock"); cfg.CentralSocket != want {
		t.Fatalf("CentralSocket=%q want %q", cfg.CentralSocket, want)
	}
}
