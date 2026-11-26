package central

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadProxyEgressStatusUsesPrivateCollectorSnapshot(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "sat-proxy-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "metrics.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(chan ProxyEgressStatus, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/snapshot" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(<-snapshot)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	svc := &Service{}
	if got := svc.DownloadProxyEgressStatus(t.Context()); got != nil {
		t.Fatal("unconfigured collector should have no data")
	}
	svc.ConfigureProxyEgressMetrics(path)
	recent := ProxyEgressStatus{SampledAt: time.Now(), Nodes: []ProxyEgress{{Protocol: "vless", Active: 3}, {Protocol: "hysteria2", Active: 1}}}
	snapshot <- recent
	if got := svc.DownloadProxyEgressStatus(t.Context()); got == nil || len(got.Nodes) != 2 || got.Nodes[0].Active != 3 {
		t.Fatalf("unexpected collector response: %+v", got)
	}
	recent.SampledAt = recent.SampledAt.Add(-time.Minute)
	snapshot <- recent
	if got := svc.DownloadProxyEgressStatus(t.Context()); got != nil {
		t.Fatal("stale metrics were accepted")
	}
}
