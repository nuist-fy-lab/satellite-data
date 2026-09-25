package transfer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
)

func TestSafeRelativeDir(t *testing.T) {
	for _, value := range []string{"goes/2026/09/24", "modis/Terra"} {
		if _, err := safeRelativeDir(value); err != nil {
			t.Fatalf("safe path %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "..", "../escape", "/absolute"} {
		if _, err := safeRelativeDir(value); err == nil {
			t.Fatalf("unsafe path %q accepted", value)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	start, end, total, ok := parseContentRange("bytes 100-199/1000")
	if !ok || start != 100 || end != 199 || total != 1000 {
		t.Fatalf("unexpected range %d-%d/%d ok=%v", start, end, total, ok)
	}
}

func TestDownloadResumesOnlyMatchingObject(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("ETag", `"original"`)
		w.Header().Set("Content-Length", "10")
		if requests == 1 {
			_, _ = w.Write([]byte("12345"))
			return
		}
		if r.Header.Get("Range") != "bytes=5-" || r.Header.Get("If-Range") != `"original"` {
			t.Errorf("unexpected resume headers: %v", r.Header)
		}
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("67890"))
	}))
	defer server.Close()
	task := model.Task{ID: 42, TaskSpec: model.TaskSpec{SourceID: "goes", Name: "file.nc", URL: server.URL, Size: 10}}
	root := t.TempDir()
	client := NewHTTP(server.Client())
	if _, err := client.Download(context.Background(), task, root, nil); err == nil {
		t.Fatal("expected interrupted first download")
	}
	result, err := client.Download(context.Background(), task, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil || string(data) != "1234567890" || requests != 2 {
		t.Fatalf("resumed content=%q requests=%d err=%v", data, requests, err)
	}
}

func TestDownloadRejectsUnidentifiedPart(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Error("unidentified part must not be resumed")
		}
		w.Header().Set("ETag", `"current"`)
		_, _ = w.Write([]byte("new content"))
	}))
	defer server.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "staging", "goes", "7")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.nc.part"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: 7, TaskSpec: model.TaskSpec{SourceID: "goes", Name: "file.nc", URL: server.URL}}
	result, err := NewHTTP(server.Client()).Download(context.Background(), task, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil || string(data) != "new content" {
		t.Fatalf("fresh content=%q err=%v", data, err)
	}
}

func TestDownloadRejectsShortPartialRange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-4/10")
		w.Header().Set("ETag", `"current"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	task := model.Task{ID: 3, TaskSpec: model.TaskSpec{SourceID: "goes", Name: "file.nc", URL: server.URL, Size: 10}}
	root := t.TempDir()
	_, err := NewHTTP(server.Client()).Download(context.Background(), task, root, nil)
	var integrity *IntegrityError
	if !errors.As(err, &integrity) {
		t.Fatalf("expected integrity error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "staging", "goes", "3", "file.nc.downloaded")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file unexpectedly ready: %v", err)
	}
}
