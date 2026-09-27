package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestERA5CLIOrdersExactlyOnceAndPolls(t *testing.T) {
	var posted struct {
		Collection string                     `json:"collection"`
		Inputs     map[string]json.RawMessage `json:"inputs"`
	}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-cli-key" {
			t.Error("CLI authorization missing")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/era5/schema"):
			_, _ = io.WriteString(w, `{"id":"reanalysis-era5-single-levels","inputs":{"variable":{}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/era5/requests":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"jobID":"job-1","status":"accepted"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/era5/requests/job-1":
			_, _ = io.WriteString(w, `{"jobID":"job-1","status":"successful"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/era5/requests/job-1/result":
			_, _ = io.WriteString(w, `{"id":"era5:job-1","name":"result.nc","size":123,"media_type":"application/x-netcdf","url":"https://cdn.example/result?secret=private"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := &client{base: server.URL, apiKey: "test-cli-key", http: &http.Client{Timeout: time.Second}}
	if err := c.era5Schema([]string{"--collection", "era5:reanalysis-era5-single-levels"}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--collection", "era5:reanalysis-era5-single-levels", "--variable", "2m_temperature", "--product-type", "reanalysis", "--year", "2024", "--month", "01", "--day", "01", "--time", "00:00", "--area", "32.25,-118.75,32,-118.5", "--data-format", "netcdf", "--download-format", "unarchived"}
	if err := c.era5Submit(args); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || posted.Collection != "era5:reanalysis-era5-single-levels" || string(posted.Inputs["variable"]) != `["2m_temperature"]` || string(posted.Inputs["data_format"]) != `"netcdf"` || string(posted.Inputs["area"]) != `[32.25,-118.75,32,-118.5]` {
		t.Fatalf("invalid CDS request: posts=%d inputs=%v", posts, posted.Inputs)
	}
	if err := c.era5Status([]string{"--id", "job-1"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	previous := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = previous }()
	if err := c.era5Result([]string{"--id", "job-1", "--json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "result.nc") || strings.Contains(string(output), "cdn.example") || strings.Contains(string(output), "secret=private") {
		t.Fatalf("unsafe CLI output %s", output)
	}
	if posts != 1 {
		t.Fatal("status or result repeated the order")
	}
}

func TestERA5CLIRejectsInvalidInputsBeforeSubmitting(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++ }))
	defer server.Close()
	c := &client{base: server.URL, http: &http.Client{Timeout: time.Second}}
	for _, args := range [][]string{
		{"--collection", "era5:reanalysis-era5-single-levels", "--area", "32,-118,33,-117"},
		{"--collection", "era5:reanalysis-era5-single-levels", "--inputs-file", "/nonexistent", "--variable", "2m_temperature"},
		{"--collection", "era5:reanalysis-era5-single-levels"},
	} {
		if err := c.era5Submit(args); err == nil {
			t.Fatalf("accepted invalid input %v", args)
		}
	}
	file := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(file, []byte(`{"variable":["2m_temperature"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// The file path can be parsed without turning provider credentials into CLI flags.
	if err := c.era5Submit([]string{"--collection", "era5:reanalysis-era5-single-levels", "--inputs-file", file}); err == nil {
		t.Fatal("expected invalid mock response")
	}
	if posts != 1 {
		t.Fatalf("expected only the valid file submission, got %d posts", posts)
	}
	if err := c.era5Status([]string{"--id", "../other"}); err == nil {
		t.Fatal("accepted invalid job ID")
	}
}
