package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/access"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/config"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/model"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/store"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/transfer"
)

func TestAllowedHostAcceptsNativeLAADS(t *testing.T) {
	for _, source := range []string{"modis", "viirs"} {
		if !allowedHost(source, "ladsweb.modaps.eosdis.nasa.gov") {
			t.Fatalf("%s should allow native LAADS archive", source)
		}
		if !allowedHost(source, "data.laadsdaac.earthdatacloud.nasa.gov") {
			t.Fatalf("%s should retain Earthdata Cloud compatibility", source)
		}
	}
	if allowedHost("modis", "example.invalid") {
		t.Fatal("unexpected host accepted")
	}
}

func TestIASIUsesTheEUMETSATDownloadHost(t *testing.T) {
	if !allowedHost("iasi", "api.eumetsat.int") {
		t.Fatal("IASI should use the official EUMETSAT Data Store host")
	}
	if allowedHost("iasi", "example.invalid") {
		t.Fatal("IASI accepted an untrusted download host")
	}
}

func TestIASINGUsesTheEUMETSATDownloadHost(t *testing.T) {
	if !allowedHost("iasi-ng", "api.eumetsat.int") {
		t.Fatal("IASI-NG should use the official EUMETSAT Data Store host")
	}
	if allowedHost("iasi-ng", "example.invalid") {
		t.Fatal("IASI-NG accepted an untrusted download host")
	}
}

func TestERA5TasksUseOnlyTheStableCentreReference(t *testing.T) {
	if !allowedHost("era5", "cds.climate.copernicus.eu") {
		t.Fatal("stable CDS job reference was rejected")
	}
	for _, host := range []string{"object-store.os-api.cci2.ecmwf.int", "127.0.0.1", "example.invalid"} {
		if allowedHost("era5", host) {
			t.Fatalf("downloader accepted direct ERA5 asset host %s", host)
		}
	}
}

func TestProviderFlowAdaptsWithinCeiling(t *testing.T) {
	flow := &providerFlow{limit: 4, gap: 200 * time.Millisecond, lastSample: time.Now().Add(-2 * time.Second)}
	for range 4 {
		if !flow.start() {
			t.Fatal("capacity lost before limit")
		}
	}
	if flow.start() {
		t.Fatal("flow exceeded concurrency limit")
	}
	flow.finish(false, true, 4)
	limit, active, _ := flow.snapshot()
	if limit != 2 || active != 3 || flow.gap != 400*time.Millisecond {
		t.Fatalf("after throttle: limit=%d active=%d", limit, active)
	}
	if time.Until(flow.nextStart) < 300*time.Millisecond {
		t.Fatalf("failure did not slow next network start: %s", time.Until(flow.nextStart))
	}
	for range 3 {
		flow.finish(true, false, 4)
	}
	for range 8 {
		if !flow.start() {
			t.Fatal("expected one active slot")
		}
		flow.finish(true, false, 4)
	}
	limit, _, _ = flow.snapshot()
	if limit != 3 {
		t.Fatalf("expected additive recovery, got %d", limit)
	}
	for range 12 {
		flow.active++
		flow.finish(false, true, 4)
	}
	if flow.limit != 1 || flow.gap != 5*time.Second {
		t.Fatalf("congestion not bounded: limit=%d gap=%s", flow.limit, flow.gap)
	}
	for range 160 {
		flow.active++
		flow.finish(true, false, 4)
	}
	if flow.limit != 4 || flow.gap != 100*time.Millisecond {
		t.Fatalf("success recovery not bounded: limit=%d gap=%s", flow.limit, flow.gap)
	}
}

func TestProviderFlowCanGrowAboveInitialLimit(t *testing.T) {
	flow := &providerFlow{limit: 4, gap: 200 * time.Millisecond}
	for range 32 {
		flow.active++
		flow.finish(true, false, 8)
	}
	if flow.limit != 8 {
		t.Fatalf("adaptive concurrency=%d want ceiling 8", flow.limit)
	}
	for range 20 {
		flow.active++
		flow.finish(false, true, 8)
	}
	if flow.limit != 1 {
		t.Fatalf("adaptive concurrency=%d want floor 1", flow.limit)
	}
}

func TestMODISExploresDuringLongSlowTransfersWithoutCompletedFiles(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{
		limit: 4, active: 4, gap: 200 * time.Millisecond,
		probeAt: now.Add(-progressProbeInterval - time.Second),
		bytes:   2 << 20,
	}
	previous, next, rate := flow.probeProgress(now, 8)
	if previous != 4 || next != 5 || rate <= 0 {
		t.Fatalf("slow but progressing requests should grow 4->5 without a full-file completion: %d %d %.2f", previous, next, rate)
	}
	if _, next, _ := flow.probeProgress(now.Add(10*time.Second), 8); next != 5 {
		t.Fatal("concurrency increased faster than the exploration interval")
	}
	for limit := 6; limit <= 8; limit++ {
		flow.active = flow.limit
		flow.addBytes(2 << 20)
		_, next, _ = flow.probeProgress(now.Add(time.Duration(limit-4)*progressProbeInterval+10*time.Second), 8)
		if next != limit {
			t.Fatalf("progressive exploration stopped at %d, want %d", next, limit)
		}
	}
	flow.active = 8
	flow.addBytes(2 << 20)
	if _, next, _ := flow.probeProgress(now.Add(8*progressProbeInterval), 8); next != 8 {
		t.Fatal("concurrency exceeded configured ceiling")
	}
}

func TestMODISProgressProbeNeedsTrafficAndFullSlots(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		active int
		delta  int64
	}{
		{"idle", 0, 2 << 20},
		{"spare_slot", 3, 2 << 20},
		{"no_progress", 4, 0},
		{"insufficient_progress", 4, 200 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := &providerFlow{limit: 4, active: tc.active, probeAt: now.Add(-time.Minute), bytes: tc.delta}
			_, next, _ := flow.probeProgress(now, 8)
			if next != 4 {
				t.Fatalf("%s grew to %d", tc.name, next)
			}
		})
	}
}

func TestMODISCongestionSuppressesExplorationUntilCooldown(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{limit: 4, active: 4, probeAt: now.Add(-time.Minute), bytes: 2 << 20, gap: 200 * time.Millisecond}
	flow.finish(false, true, 8)
	if flow.limit != 2 {
		t.Fatalf("true upstream congestion must still reduce concurrency, got %d", flow.limit)
	}
	flow.active = flow.limit
	flow.addBytes(4 << 20)
	if _, next, _ := flow.probeProgress(now.Add(time.Minute), 8); next != 2 {
		t.Fatalf("probe ignored upstream congestion cooldown: %d", next)
	}
	flow.addBytes(4 << 20)
	if _, next, _ := flow.probeProgress(now.Add(4*progressProbeInterval), 8); next != 3 {
		t.Fatalf("probe never recovered after cooldown: %d", next)
	}
}

func TestMODISProgressRecoveryDecaysCongestionOnlyOnUsefulIntervals(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{limit: 2, active: 2, errorRate: 0.5, probeAt: now, probeBlockedUntil: now.Add(progressProbeInterval)}
	flow.addBytes(2 << 20)
	flow.probeProgress(now.Add(progressProbeInterval/2), 8)
	if flow.errorRate != 0.5 || flow.limit != 2 {
		t.Fatal("cooldown must preserve congestion history")
	}
	flow.probeProgress(now.Add(progressProbeInterval), 8)
	if flow.errorRate != 0.4 || flow.limit != 2 {
		t.Fatalf("first useful interval should decay without premature growth: rate=%f limit=%d", flow.errorRate, flow.limit)
	}
	flow.probeProgress(now.Add(2*progressProbeInterval), 8)
	if flow.errorRate != 0.4 || flow.limit != 2 {
		t.Fatal("an interval without useful progress must not decay congestion")
	}
	for i := 3; i <= 6; i++ {
		flow.addBytes(2 << 20)
		flow.probeProgress(now.Add(time.Duration(i)*progressProbeInterval), 8)
	}
	if flow.limit != 3 || flow.errorRate >= 0.2 {
		t.Fatalf("sustained progress should permit one bounded probe: rate=%f limit=%d", flow.errorRate, flow.limit)
	}
}

func TestMODISRecoveryAfterMissingObjectAndIdle(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{limit: 1, active: 1, errorRate: 0.2, probeAt: now}
	flow.finish(false, isCongestion(&transfer.HTTPError{Status: 404}), 8)
	flow.probeProgress(now.Add(progressProbeInterval), 8)
	if flow.limit != 1 || flow.errorRate != 0.2 {
		t.Fatalf("404 and idle must not change congestion history: limit=%d rate=%f", flow.limit, flow.errorRate)
	}
	if !flow.start() {
		t.Fatal("expected a slot for the next transfer")
	}
	flow.addBytes(2 << 20)
	flow.probeProgress(now.Add(2*progressProbeInterval), 8)
	if flow.limit != 2 || flow.errorRate >= 0.2 {
		t.Fatalf("productive transfer should recover without completing: limit=%d rate=%f", flow.limit, flow.errorRate)
	}
}

func TestProviderLowThroughputThresholdUsesRecentMedian(t *testing.T) {
	flow := &providerFlow{}
	policy := config.LowThroughputPolicy{
		FloorBytesPerSecond: 64 << 10,
		BaselineFraction:    0.15,
	}
	if got := flow.lowThroughputThreshold(policy); got != 64<<10 {
		t.Fatalf("empty baseline threshold=%d want %d", got, 64<<10)
	}
	for _, rate := range []int64{4 << 20, 6 << 20, 5 << 20} {
		flow.observeTransferRate(rate*10, 10*time.Second, policy.FloorBytesPerSecond)
	}
	want := int64(float64(5<<20) * 0.15)
	if got := flow.lowThroughputThreshold(policy); got != want {
		t.Fatalf("adaptive threshold=%d want %d", got, want)
	}
}

func TestProviderPacingAndCooldown(t *testing.T) {
	flow := &providerFlow{limit: 2, gap: time.Hour}
	if err := flow.pace(t.Context()); err != nil {
		t.Fatal(err)
	}
	flow.cooldown(2 * time.Hour)
	if time.Until(flow.nextStart) > time.Hour || time.Until(flow.nextStart) < 59*time.Minute {
		t.Fatalf("Retry-After cap not honored: %s", time.Until(flow.nextStart))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := flow.pace(ctx); err == nil {
		t.Fatal("pace should honor cancellation")
	}
}

func TestProviderActiveTransfersUsesDownloadingTasks(t *testing.T) {
	if got := providerActiveTransfers(map[string]int{"done": 66}); got != 0 {
		t.Fatalf("idle provider active transfers=%d want 0", got)
	}
	if got := providerActiveTransfers(map[string]int{"downloading": 3, "queued": 20}); got != 3 {
		t.Fatalf("active transfers=%d want 3", got)
	}
}

func TestCongestionClassification(t *testing.T) {
	if !isCongestion(&transfer.HTTPError{Status: 429}) || !isCongestion(&transfer.HTTPError{Status: 503}) {
		t.Fatal("rate limiting and server errors should reduce concurrency")
	}
	if isCongestion(&transfer.HTTPError{Status: 404}) {
		t.Fatal("missing objects should not reduce concurrency")
	}
	if isCongestion(&transfer.LowThroughputError{}) {
		t.Fatal("one recycled slow connection should not reduce provider concurrency")
	}
}

func TestHighCeilingBurst502CutsOncePerEpochAndSustainedCongestionStillHalves(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{limit: 72, initial: 36, active: 10, gap: 200 * time.Millisecond}

	// Four congested completions within 2s must halve once (72->36) instead of collapsing.
	for i := 0; i < 4; i++ {
		flow.finishAt(now.Add(time.Duration(i)*500*time.Millisecond), false, true, 72)
	}
	if flow.limit != 36 {
		t.Fatalf("burst 502 collapsed limit to %d, want 36", flow.limit)
	}
	if flow.errorRate > 0.5 {
		t.Fatalf("high-ceiling errorRate=%.3f exceeded cap 0.5", flow.errorRate)
	}

	// Sustained congestion after congestionCutEpoch (10s) still halves (36->18).
	flow.active++
	flow.finishAt(now.Add(11*time.Second), false, true, 72)
	if flow.limit != 18 {
		t.Fatalf("sustained congestion limit=%d, want 18", flow.limit)
	}
}

func TestHighCeilingFastRecoveryViaCompletionsAndProgressProbe(t *testing.T) {
	now := time.Now()
	flow := &providerFlow{limit: 2, initial: 36, errorRate: 0.5, gap: 400 * time.Millisecond}

	for i := 0; i < 45; i++ {
		flow.active++
		flow.finishAt(now.Add(time.Duration(i)*time.Second), true, false, 72)
	}
	if flow.limit < 36 || flow.limit > 72 {
		t.Fatalf("expected fast recovery to >=36 within 45 completions, got %d", flow.limit)
	}
	for i := 45; i < 140; i++ {
		flow.active++
		flow.finishAt(now.Add(time.Duration(i)*time.Second), true, false, 72)
	}
	if flow.limit != 72 {
		t.Fatalf("expected recovery bounded at ceiling 72, got %d", flow.limit)
	}

	probeFlow := &providerFlow{limit: 2, initial: 36, active: 2, probeAt: now, gap: 200 * time.Millisecond}
	for i := 1; i <= 8; i++ {
		probeFlow.active = probeFlow.limit
		probeFlow.addBytes(2 << 20)
		probeFlow.probeProgress(now.Add(time.Duration(i)*progressProbeInterval), 72)
	}
	if probeFlow.limit < 36 || probeFlow.limit > 72 {
		t.Fatalf("expected probeProgress fast recovery to >=36 in 8 intervals, got %d", probeFlow.limit)
	}
}

func TestPausedTransfersStillPromoteStagedFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "queue.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer db.Close()
	defer cancel()
	manager, err := access.NewManager(filepath.Join(root, "central.sock"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		TransfersPaused: true,
		ArchiveRoot:     filepath.Join(root, "archive"),
		Providers:       []config.Provider{{SourceID: "modis", Enabled: true, TransferConcurrency: 2, VerifyConcurrency: 1}},
	}
	if err := os.Mkdir(cfg.ArchiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(cfg, db, manager)
	if err != nil {
		t.Fatal(err)
	}
	runtime.official = nil
	spec := model.TaskSpec{Key: "staged", SourceID: "modis", Name: "staged.hdf", RelativeDir: "fixtures", URL: "https://example.invalid/staged.hdf"}
	ready, _, err := db.Enqueue(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "staged.hdf")
	if err := os.WriteFile(staged, []byte("staged content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkDownloaded(ctx, ready.ID, staged, 14); err != nil {
		t.Fatal(err)
	}
	spec.Key, spec.Name = "queued", "queued.hdf"
	queued, _, err := db.Enqueue(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Run(ctx)
	defer func() { cancel(); runtime.Wait() }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := db.Task(ctx, ready.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.State == "done" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("staged file stayed in %s: %s", task.State, task.LastError)
		case <-ticker.C:
		}
	}
	task, err := db.Task(ctx, queued.ID)
	if err != nil || task.State != "queued" || task.Attempts != 0 {
		t.Fatalf("paused transfer was claimed: %+v, %v", task, err)
	}
}
