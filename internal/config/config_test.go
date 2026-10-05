package config

import (
	"path/filepath"
	"testing"
)

func TestGOESAdaptiveTransferDefaults(t *testing.T) {
	t.Setenv("SATDOWN_GOES_INITIAL_TRANSFER_CONCURRENCY", "")
	t.Setenv("SATDOWN_GOES_TRANSFER_CONCURRENCY", "")
	cfg := Load()
	for _, p := range cfg.Providers {
		if p.SourceID == "goes" {
			if p.InitialTransferConcurrency != 4 || p.TransferConcurrency != 8 || p.VerifyConcurrency != 2 {
				t.Fatalf("invalid GOES defaults: %+v", p)
			}
			return
		}
	}
	t.Fatal("GOES transfer worker missing")
}

func TestInitialTransferConcurrencyNeverExceedsCeiling(t *testing.T) {
	t.Setenv("SATDOWN_GOES_TRANSFER_CONCURRENCY", "3")
	t.Setenv("SATDOWN_GOES_INITIAL_TRANSFER_CONCURRENCY", "8")
	for _, p := range Load().Providers {
		if p.SourceID == "goes" {
			if p.InitialTransferConcurrency != 3 || p.TransferConcurrency != 3 {
				t.Fatalf("GOES concurrency was not clamped to ceiling: %+v", p)
			}
			return
		}
	}
	t.Fatal("GOES transfer worker missing")
}

func TestERA5WorkerUsesCentreIPC(t *testing.T) {
	cfg := Load()
	for _, p := range cfg.Providers {
		if p.SourceID == "era5" {
			if !p.Enabled || p.InitialTransferConcurrency != 2 || p.TransferConcurrency != 2 || p.VerifyConcurrency != 2 || p.AuthRef != "cds" {
				t.Fatalf("invalid ERA5 defaults: %+v", p)
			}
			return
		}
	}
	t.Fatal("ERA5 transfer worker missing")
}

func TestArchiveSharingDefaultAndOptOut(t *testing.T) {
	t.Setenv("SATDOWN_ARCHIVE_GROUP_WRITABLE", "")
	if !Load().ArchiveGroupWritable {
		t.Fatal("default archive must allow group write and public read")
	}
	t.Setenv("SATDOWN_ARCHIVE_GROUP_WRITABLE", "false")
	if Load().ArchiveGroupWritable {
		t.Fatal("explicit private archive policy must be respected")
	}
}

func TestLoadUsesXDGRunTimeForIPC(t *testing.T) {
	home := t.TempDir()
	runtime := filepath.Join(t.TempDir(), "run")
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("SATDOWN_SOCKET", "")
	t.Setenv("SATDOWN_CENTRAL_SOCKET", "")
	t.Setenv("SATDOWN_ARCHIVE_EVENT_SOCKET", "")

	cfg := Load()
	if want := filepath.Join(runtime, "satellite-data-downloader", "downloader.sock"); cfg.SocketPath != want {
		t.Fatalf("SocketPath=%q want %q", cfg.SocketPath, want)
	}
	if want := filepath.Join(runtime, "satellite-data", "central.sock"); cfg.CentralSocket != want {
		t.Fatalf("CentralSocket=%q want %q", cfg.CentralSocket, want)
	}
	if want := filepath.Join(runtime, "satellite-data", "archive-events.sock"); cfg.ArchiveEventSocket != want {
		t.Fatalf("ArchiveEventSocket=%q want %q", cfg.ArchiveEventSocket, want)
	}
	if want := filepath.Join(home, ".local", "state", "satellite-data-downloader", "downloader.sqlite3"); cfg.DBPath != want {
		t.Fatalf("DBPath=%q want %q", cfg.DBPath, want)
	}
}

func TestLoadFallsBackToHomeStateForIPC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("SATDOWN_SOCKET", "")
	t.Setenv("SATDOWN_CENTRAL_SOCKET", "")
	t.Setenv("SATDOWN_ARCHIVE_EVENT_SOCKET", "")

	cfg := Load()
	root := filepath.Join(home, ".local", "state")
	if want := filepath.Join(root, "satellite-data-downloader", "downloader.sock"); cfg.SocketPath != want {
		t.Fatalf("SocketPath=%q want %q", cfg.SocketPath, want)
	}
	if want := filepath.Join(root, "satellite-data", "central.sock"); cfg.CentralSocket != want {
		t.Fatalf("CentralSocket=%q want %q", cfg.CentralSocket, want)
	}
	if want := filepath.Join(root, "satellite-data", "archive-events.sock"); cfg.ArchiveEventSocket != want {
		t.Fatalf("ArchiveEventSocket=%q want %q", cfg.ArchiveEventSocket, want)
	}
}
