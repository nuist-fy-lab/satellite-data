package egress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFixture(t *testing.T, cfg Config) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "egress.json")
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCountsPublicIPsAndRejectsUnsafeProxies(t *testing.T) {
	cfg := Config{Routes: []Route{
		{ID: "vwan1", Proxy: "socks5://127.0.0.1:13881", PublicIP: "58.212.61.218"},
		{ID: "vwan2", Proxy: "socks5://127.0.0.1:13882", PublicIP: "58.212.61.218"},
		{ID: "vwan3", Proxy: "socks5://127.0.0.1:13883", PublicIP: "58.212.60.213"},
	}}
	loaded, err := Load(writeFixture(t, cfg))
	if err != nil || loaded.UniquePublicIPs() != 2 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	for _, proxy := range []string{
		"socks5://8.8.8.8:1080", "socks5://user:pass@127.0.0.1:13881", "http://127.0.0.1:13881", "socks5://127.0.0.1:0", "socks5://127.0.0.1:65536",
	} {
		bad := cfg
		bad.Routes = append([]Route(nil), cfg.Routes...)
		bad.Routes[0].Proxy = proxy
		if _, err := Load(writeFixture(t, bad)); err == nil {
			t.Errorf("accepted unsafe proxy %q", proxy)
		}
	}
}

func TestSharedPublicIPUsesOneSourceQuota(t *testing.T) {
	cfg := &Config{Routes: []Route{
		{ID: "a", Proxy: "socks5://127.0.0.1:13881", PublicIP: "58.212.61.218"},
		{ID: "b", Proxy: "socks5://127.0.0.1:13882", PublicIP: "58.212.61.218"},
		{ID: "c", Proxy: "socks5://127.0.0.1:13883", PublicIP: "58.212.60.213"},
	}, PerIPLimits: map[string]int{"modis": 1}}
	p := NewPool(cfg)
	first, releaseFirst, err := p.Acquire(context.Background(), "modis")
	if err != nil || first.ID != "a" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, releaseSecond, err := p.Acquire(context.Background(), "modis")
	if err != nil || second.ID != "c" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Millisecond)
	defer cancel()
	if _, release, err := p.Acquire(ctx, "modis"); err == nil {
		release()
		t.Fatal("duplicate public IP bypassed quota")
	}
	releaseFirst()
	third, releaseThird, err := p.Acquire(context.Background(), "modis")
	if err != nil || third.PublicIP != first.PublicIP {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	releaseSecond()
	releaseThird()
	for _, item := range p.Snapshot() {
		if item.ActiveBySrc["modis"] != 0 {
			t.Fatalf("quota leaked: %+v", item)
		}
	}
}

func TestNetworkFailureCoolsOnlyOneRoute(t *testing.T) {
	p := NewPool(&Config{Routes: []Route{
		{ID: "one", Proxy: "socks5://127.0.0.1:13881", PublicIP: "58.212.61.218"},
		{ID: "two", Proxy: "socks5://127.0.0.1:13882", PublicIP: "58.212.60.213"},
	}})
	p.MarkNetworkFailure("one")
	r, release, err := p.Acquire(context.Background(), "goes")
	if err != nil || r.ID != "two" {
		t.Fatalf("route=%+v err=%v", r, err)
	}
	release()
}
