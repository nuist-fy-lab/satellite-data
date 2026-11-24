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

func newTimedPool(cfg *Config, start time.Time) (*Pool, *time.Time) {
	p := NewPool(cfg)
	clock := start
	p.now = func() time.Time { return clock }
	return p, &clock
}

func TestAcquirePrefersFasterRoutesForSource(t *testing.T) {
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	p, clock := newTimedPool(&Config{
		Routes: []Route{
			{ID: "fast", Proxy: "socks5://127.0.0.1:13880", PublicIP: "198.51.100.1"},
			{ID: "slow", Proxy: "socks5://127.0.0.1:13881", PublicIP: "198.51.100.2"},
		},
		PerIPLimits: map[string]int{"goes": 2, "modis": 2},
	}, start)

	r1, rel1, err := p.Acquire(context.Background(), "goes")
	if err != nil || r1.ID != "fast" {
		t.Fatalf("expected unmeasured fast first, got %+v err=%v", r1, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("goes", "fast", 2*(500<<10))
	rel1()

	r2, rel2, err := p.Acquire(context.Background(), "goes")
	if err != nil || r2.ID != "slow" {
		t.Fatalf("expected unmeasured slow second, got %+v err=%v", r2, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("goes", "slow", 2*(40<<10))
	rel2()

	// Both fast transfers should prefer "fast" up to its per-IP limit (2) before spilling to "slow".
	g1, gRel1, err := p.Acquire(context.Background(), "goes")
	if err != nil || g1.ID != "fast" {
		t.Fatalf("goes pick 1=%+v err=%v", g1, err)
	}
	g2, gRel2, err := p.Acquire(context.Background(), "goes")
	if err != nil || g2.ID != "fast" {
		t.Fatalf("goes pick 2=%+v err=%v", g2, err)
	}
	g3, gRel3, err := p.Acquire(context.Background(), "goes")
	if err != nil || g3.ID != "slow" {
		t.Fatalf("goes pick 3 (spill)=%+v err=%v", g3, err)
	}
	gRel1()
	gRel2()
	gRel3()

	// Source isolation: slow "slow" for goes must not affect modis when modis measures "slow" as faster.
	m1, mRel1, err := p.Acquire(context.Background(), "modis")
	if err != nil || m1.ID != "fast" {
		t.Fatalf("modis explore 1=%+v err=%v", m1, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("modis", "fast", 2*(30<<10))
	mRel1()

	m2, mRel2, err := p.Acquire(context.Background(), "modis")
	if err != nil || m2.ID != "slow" {
		t.Fatalf("modis explore 2=%+v err=%v", m2, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("modis", "slow", 2*(400<<10))
	mRel2()

	mPick, mPickRel, err := p.Acquire(context.Background(), "modis")
	if err != nil || mPick.ID != "slow" {
		t.Fatalf("modis should prefer slow route (faster for modis), got %+v err=%v", mPick, err)
	}
	mPickRel()

	gPick, gPickRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || gPick.ID != "fast" {
		t.Fatalf("goes should still prefer fast route, got %+v err=%v", gPick, err)
	}
	gPickRel()
}

func TestAcquireProbesStaleSlowRouteAndPromotesRecovery(t *testing.T) {
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	p, clock := newTimedPool(&Config{
		Routes: []Route{
			{ID: "fast", Proxy: "socks5://127.0.0.1:13880", PublicIP: "198.51.100.1"},
			{ID: "slow1", Proxy: "socks5://127.0.0.1:13881", PublicIP: "198.51.100.2"},
			{ID: "slow2", Proxy: "socks5://127.0.0.1:13882", PublicIP: "198.51.100.3"},
		},
		PerIPLimits: map[string]int{"goes": 2},
	}, start)

	for _, sample := range []struct {
		wantID string
		kbps   int
	}{
		{"fast", 500},
		{"slow1", 40},
		{"slow2", 40},
	} {
		r, rel, err := p.Acquire(context.Background(), "goes")
		if err != nil || r.ID != sample.wantID {
			t.Fatalf("initial explore got %+v want %s err=%v", r, sample.wantID, err)
		}
		*clock = clock.Add(2 * time.Second)
		p.RecordSourceBytes("goes", sample.wantID, 2*(sample.kbps<<10))
		rel()
	}

	// Keep fast fresh at +1m so only slow1 and slow2 become stale (>= 2m).
	*clock = start.Add(1 * time.Minute)
	keepFresh, keepFreshRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || keepFresh.ID != "fast" {
		t.Fatalf("expected fast before staleness, got %+v err=%v", keepFresh, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("goes", "fast", 2*(500<<10))
	keepFreshRel()

	// After sourceRouteProbeInterval (2m), the oldest idle stale route (slow1) is probed once.
	*clock = start.Add(2*time.Minute + 10*time.Second)
	probe, probeRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || probe.ID != "slow1" {
		t.Fatalf("expected stale probe of slow1, got %+v err=%v", probe, err)
	}

	// During sourceProbeCooldown (15s), slow2 is not probed; fast route is selected instead.
	*clock = start.Add(2*time.Minute + 15*time.Second)
	duringCooldown, duringRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || duringCooldown.ID != "fast" {
		t.Fatalf("expected fast during probe cooldown, got %+v err=%v", duringCooldown, err)
	}
	duringRel()

	// Simulate slow1 recovering to high throughput (~2500 KiB/s -> EMA exceeds fast's 500 KiB/s).
	*clock = start.Add(2*time.Minute + 20*time.Second)
	p.RecordSourceBytes("goes", "slow1", 10*(2500<<10))
	probeRel()

	// Within probe cooldown so slow2 isn't probed; recovered slow1 now outranks fast.
	*clock = start.Add(2*time.Minute + 21*time.Second)
	promoted, promotedRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || promoted.ID != "slow1" {
		t.Fatalf("expected recovered slow1 to outrank fast, got %+v err=%v", promoted, err)
	}
	promotedRel()
}

func TestAcquireDecaysStalledActiveRoute(t *testing.T) {
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	p, clock := newTimedPool(&Config{
		Routes: []Route{
			{ID: "routeA", Proxy: "socks5://127.0.0.1:13880", PublicIP: "198.51.100.1"},
			{ID: "routeB", Proxy: "socks5://127.0.0.1:13881", PublicIP: "198.51.100.2"},
		},
		PerIPLimits: map[string]int{"goes": 4},
	}, start)

	rA, relA, err := p.Acquire(context.Background(), "goes")
	if err != nil || rA.ID != "routeA" {
		t.Fatalf("init routeA=%+v err=%v", rA, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("goes", "routeA", 2*(500<<10))
	relA()

	rB, relB, err := p.Acquire(context.Background(), "goes")
	if err != nil || rB.ID != "routeB" {
		t.Fatalf("init routeB=%+v err=%v", rB, err)
	}
	*clock = clock.Add(2 * time.Second)
	p.RecordSourceBytes("goes", "routeB", 2*(400<<10))
	relB()

	// Acquire routeA and stall with 0 bytes transferred for 40s.
	*clock = clock.Add(1 * time.Second)
	stalled, stalledRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || stalled.ID != "routeA" {
		t.Fatalf("expected routeA before stall, got %+v err=%v", stalled, err)
	}
	*clock = clock.Add(40 * time.Second)

	next, nextRel, err := p.Acquire(context.Background(), "goes")
	if err != nil || next.ID != "routeB" {
		t.Fatalf("expected stalled routeA to decay below routeB, got %+v err=%v", next, err)
	}
	nextRel()
	stalledRel()
}

func TestMODISBackupProbesAndSwitchesUsingActualThroughput(t *testing.T) {
	p, now := newTimedPool(&Config{
		Routes: []Route{
			{ID: "direct", PublicIP: "198.51.100.1"},
			{ID: "backup", PublicIP: "198.51.100.2", Backup: true, Sources: []string{"modis"}},
		},
		PerIPLimits: map[string]int{"modis": 3},
	}, time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC))
	take := func(source, want string) func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		route, release, err := p.Acquire(ctx, source)
		if err != nil || route.ID != want {
			t.Fatalf("acquire %s: got %s, want %s, err=%v", source, route.ID, want, err)
		}
		return release
	}
	sample := func(route string, release func(), n int) {
		*now = now.Add(2 * time.Second)
		p.RecordSourceBytes("modis", route, n)
		release()
	}
	if got := p.cfg.UniquePublicIPs(); got != 1 {
		t.Fatalf("backup increased the worker ceiling: %d", got)
	}
	take("viirs", "direct")()
	sample("direct", take("modis", "direct"), 4<<20)
	sample("backup", take("modis", "backup"), 1<<20)
	take("modis", "direct")()

	// Slow primary samples and an occasional fresh trial make the faster backup eligible.
	*now = now.Add(3 * time.Minute)
	sample("direct", take("modis", "direct"), 32<<10)
	sample("backup", take("modis", "backup"), 16<<20)
	sample("backup", take("modis", "backup"), 16<<20)
	take("viirs", "direct")()

	// The primary is still re-probed and wins again when it recovers.
	*now = now.Add(3 * time.Minute)
	sample("direct", take("modis", "direct"), 256<<20)
	sample("backup", take("modis", "backup"), 1<<20)
	take("modis", "direct")()
}

func TestFailedBackupProbeDoesNotCaptureFollowingTransfers(t *testing.T) {
	p, now := newTimedPool(&Config{Routes: []Route{
		{ID: "direct", PublicIP: "198.51.100.1"},
		{ID: "backup", PublicIP: "198.51.100.2", Backup: true, Sources: []string{"modis"}},
	}}, time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC))
	r, release, err := p.Acquire(t.Context(), "modis")
	if err != nil || r.ID != "direct" {
		t.Fatalf("first route: %s %v", r.ID, err)
	}
	*now = now.Add(time.Second)
	p.RecordSourceBytes("modis", r.ID, 1024)
	release()
	probe, done, err := p.Acquire(t.Context(), "modis")
	if err != nil || probe.ID != "backup" {
		t.Fatalf("probe route: %s %v", probe.ID, err)
	}
	p.MarkNetworkFailure(probe.ID)
	done()
	*now = now.Add(time.Minute)
	after, done, err := p.Acquire(t.Context(), "modis")
	if err != nil || after.ID != "direct" {
		t.Fatalf("route after failed probe: %s %v", after.ID, err)
	}
	done()
}

func TestSnapshotUsesActualRouteCountsAndSourceRates(t *testing.T) {
	p, now := newTimedPool(&Config{Routes: []Route{
		{ID: "direct", PublicIP: "198.51.100.1"},
		{ID: "vwan1", PublicIP: "198.51.100.1"},
	}}, time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC))
	first, done, err := p.Acquire(t.Context(), "modis")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	second, doneSecond, err := p.Acquire(t.Context(), "modis")
	if err != nil {
		t.Fatal(err)
	}
	defer doneSecond()
	if first.ID == second.ID {
		t.Fatal("expected two measured route identities")
	}
	p.Snapshot()
	*now = now.Add(5 * time.Second)
	p.RecordSourceBytes("modis", first.ID, 625000)
	p.RecordSourceBytes("modis", second.ID, 1250000)
	got := p.Snapshot()
	for i, want := range []float64{1, 2} {
		row := got[i]
		rate := row.RatesBySource["modis"]
		if row.ActiveBySrc["modis"] != 1 || row.ActiveTransfers != 1 {
			t.Fatalf("shared IP duplicated active counts on %s: %+v", row.ID, row)
		}
		if rate.DownloadMbps == nil || rate.SmoothedMbps == nil || *rate.DownloadMbps != want || *rate.SmoothedMbps != want {
			t.Fatalf("unexpected rate on %s: %+v", row.ID, rate)
		}
	}
}

func TestBackupProviderFailureIsIsolated(t *testing.T) {
	p, now := newTimedPool(&Config{Routes: []Route{
		{ID: "direct", PublicIP: "198.51.100.1"},
		{ID: "backup", PublicIP: "198.51.100.2", Backup: true, Sources: []string{"modis", "viirs"}},
	}}, time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC))
	take := func(source, want string) func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		route, release, err := p.Acquire(ctx, source)
		if err != nil || route.ID != want {
			t.Fatalf("acquire %s: got %s, want %s, err=%v", source, route.ID, want, err)
		}
		return release
	}
	for _, source := range []string{"modis", "viirs"} {
		release := take(source, "direct")
		*now = now.Add(2 * time.Second)
		p.RecordSourceBytes(source, "direct", 100000)
		release()
	}
	failed := take("modis", "backup")
	p.MarkSourceFailure("modis", "backup")
	failed()
	take("modis", "direct")()
	take("viirs", "backup")()
	s := p.Snapshot()[1]
	if s.Blocked || !s.BlockedBySource["modis"] || s.BlockedBySource["viirs"] {
		t.Fatalf("incorrect backup cooldown: %+v", s)
	}
}
