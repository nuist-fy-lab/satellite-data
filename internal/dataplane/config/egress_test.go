package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadEgressScalesByUniquePublicIP(t *testing.T) {
	p := filepath.Join(t.TempDir(), "egress.json")
	data := `{"routes":[{"id":"one","proxy":"socks5://127.0.0.1:13881","public_ip":"58.212.61.218"},{"id":"two","proxy":"socks5://127.0.0.1:13882","public_ip":"58.212.61.218"},{"id":"three","proxy":"socks5://127.0.0.1:13883","public_ip":"58.212.60.213"}]}`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SATELLITE_DOWNLOAD_EGRESS_FILE", p)
	t.Setenv("SATDOWN_MODIS_TRANSFER_CONCURRENCY", "4")
	t.Setenv("SATDOWN_MODIS_INITIAL_TRANSFER_CONCURRENCY", "4")
	c := Load()
	if c.EgressConfigError != nil || c.EgressUniqueIPs != 2 {
		t.Fatalf("config error=%v count=%d", c.EgressConfigError, c.EgressUniqueIPs)
	}
	for _, v := range c.Providers {
		if v.SourceID == "modis" && (v.TransferConcurrency != 8 || v.InitialTransferConcurrency != 8) {
			t.Fatalf("modis incorrectly scaled: %+v", v)
		}
		if v.SourceID == "goes" && (v.TransferConcurrency != 16 || v.InitialTransferConcurrency != 8) {
			t.Fatalf("goes incorrectly scaled: %+v", v)
		}
	}
}

func TestInvalidDownloadEgressFailsClosed(t *testing.T) {
	t.Setenv("SATELLITE_DOWNLOAD_EGRESS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	if cfg := Load(); cfg.EgressConfigError == nil {
		t.Fatal("missing configured egress should fail closed")
	}
}
