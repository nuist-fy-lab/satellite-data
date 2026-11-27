package linkmon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestProbeDelegatesDNSLookupToDirectSOCKS5(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	const target = "unresolvable.invalid"
	server := make(chan error, 1)
	go func() {
		server <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				return err
			}
			greeting := make([]byte, 3)
			if _, err := io.ReadFull(conn, greeting); err != nil {
				return err
			}
			if !bytes.Equal(greeting, []byte{5, 1, 0}) {
				return errors.New("unexpected SOCKS5 greeting")
			}
			if _, err := conn.Write([]byte{5, 0}); err != nil {
				return err
			}
			want := append([]byte{5, 1, 0, 3, byte(len(target))}, target...)
			want = append(want, 1, 187) // port 443
			got := make([]byte, len(want))
			if _, err := io.ReadFull(conn, got); err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				return errors.New("CONNECT did not pass the original hostname")
			}
			_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
			return err
		}()
	}()
	monitor, err := New(nil, "socks5://"+listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	monitor.attempts = 1
	sample := monitor.probe(t.Context(), Target{Host: target, Port: 443})
	if sample.SuccessCount != 1 || sample.LossPct != 0 || sample.ResolvedIP != "" {
		t.Fatalf("unexpected SOCKS5 link sample: %+v", sample)
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

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
