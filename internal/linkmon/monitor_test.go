package linkmon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestJitter(t *testing.T) {
	got := jitter([]float64{100, 120, 110, 130})
	if got != 50.0/3.0 {
		t.Fatalf("jitter=%v", got)
	}
}

func TestTargetsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, target := range Targets() {
		if target.SourceID == "" || target.Host == "" || target.Port <= 0 {
			t.Fatalf("invalid target %#v", target)
		}
		if seen[target.SourceID] {
			t.Fatalf("duplicate source %s", target.SourceID)
		}
		seen[target.SourceID] = true
	}
}

func TestSharedHostUsesSameProbeSample(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	monitor := &Monitor{store: store, attempts: 1, timeout: time.Second, targets: []Target{
		{SourceID: "one", Host: "127.0.0.1", Port: port},
		{SourceID: "two", Host: "127.0.0.1", Port: port},
	}}
	monitor.probeAll(context.Background())
	samples, err := store.LatestLinkSamples(context.Background())
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	if !samples[0].SampledAt.Equal(samples[1].SampledAt) || samples[0].LossPct != samples[1].LossPct {
		t.Fatalf("same host must share one observation: %+v", samples)
	}
}
