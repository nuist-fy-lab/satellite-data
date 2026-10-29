package access

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortUnixSocket(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sat-ipc-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func TestCentralIPCStreamsRangesWithoutAnyCredential(t *testing.T) {
	path := shortUnixSocket(t, "central.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/access/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"providers":{"modis":true,"cloudsat":false}}`)
	})
	mux.HandleFunc("POST /v1/fetch", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad IPC JSON")
		}
		if body["source_id"] != "modis" || body["range"] != "bytes=3-" || body["if_range"] != "fixture-etag" {
			t.Error("lost resume headers")
		}
		if len(body) != 4 || r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("credentials crossed IPC")
		}
		w.Header().Set("ETag", "fixture-etag")
		w.Header().Set("Content-Range", "bytes 3-5/6")
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(206)
		io.WriteString(w, "def")
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SATDOWN_CLOUDSAT_USERNAME", "ignored-fixture")
	t.Setenv("SATDOWN_CLOUDSAT_PASSWORD", "ignored-fixture")
	if !manager.Configured(t.Context(), "modis", "laads") || manager.Configured(t.Context(), "cloudsat", "cloudsat") {
		t.Fatal("legacy credential fallback or wrong centre status")
	}
	route, err := manager.Resolve(t.Context(), "modis", "laads", time.Second)
	if err != nil || route.Authorizer != nil || route.CredentialID != nil {
		t.Fatal("downloader unexpectedly owns authorization")
	}
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://ladsweb.modaps.eosdis.nasa.gov/fixture.h5", nil)
	r.Header.Set("Range", "bytes=3-")
	r.Header.Set("If-Range", "fixture-etag")
	response, err := (&centralTransport{manager: manager, source: "modis"}).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 206 || string(data) != "def" || response.Header.Get("ETag") != "fixture-etag" || response.Request != r {
		t.Fatal("IPC response changed")
	}
	server.Close()
	manager.client.CloseIdleConnections()
	manager.invalidate()
	if manager.Configured(context.Background(), "modis", "laads") {
		t.Fatal("offline centre fell back to a credential")
	}
	if _, err := manager.Resolve(context.Background(), "modis", "laads", time.Second); !errors.Is(err, ErrCentralUnavailable) {
		t.Fatalf("offline centre error=%v want ErrCentralUnavailable", err)
	}
}

func TestCentralTransportRefusesMutation(t *testing.T) {
	manager, _ := NewManager("/nonexistent/central.sock")
	r, _ := http.NewRequest(http.MethodPost, "https://example.test/", strings.NewReader("data"))
	if _, err := (&centralTransport{manager: manager, source: "modis"}).RoundTrip(r); err == nil {
		t.Fatal("mutation forwarded to centre")
	}
}

func TestCentralTransportReportsUnavailableCentre(t *testing.T) {
	manager, _ := NewManager(shortUnixSocket(t, "missing.sock"))
	r, _ := http.NewRequest(http.MethodGet, "https://storage.googleapis.com/fixture", nil)
	if _, err := (&centralTransport{manager: manager, source: "goes"}).RoundTrip(r); !errors.Is(err, ErrCentralUnavailable) {
		t.Fatalf("transport error=%v want ErrCentralUnavailable", err)
	}
}
