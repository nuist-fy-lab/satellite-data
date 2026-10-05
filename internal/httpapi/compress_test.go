package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressionGzipsTextResponses(t *testing.T) {
	handler := withCompression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"payload":"`+strings.Repeat("abcdef", 100)+`"}`)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("content encoding=%q want gzip", rec.Header().Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"payload"`) {
		t.Fatalf("unexpected body %q", body)
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("compressed response kept content-length=%q", rec.Header().Get("Content-Length"))
	}
}

func TestCompressionSkipsStreams(t *testing.T) {
	handler := withCompression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ok\n\n")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("stream content encoding=%q want empty", got)
	}
}

func TestVersionedStaticAssetsAreImmutable(t *testing.T) {
	handler := withHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, "export default 1")
	}))
	req := httptest.NewRequest(http.MethodGet, "/app.js?v=21", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("document cache-control=%q want no-store", got)
	}
}
