package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDownloadProvidersOmitsAccessDetails(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/downloader/providers" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"source_id":"goes18","enabled":true,"auth_configured":true,"auth_ref":"internal-account-ref","credentials":{"alias":"internal-credential"},"states":{"queued":2}}]}`))
	}))
	defer server.Close()

	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	c := &client{base: server.URL, http: &http.Client{Timeout: time.Second}}
	if err := c.downloadProviders(); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if requests != 1 || !strings.Contains(text, "goes18") || !strings.Contains(text, "configured=true") || !strings.Contains(text, "queued:2") {
		t.Fatalf("requests=%d output=%q", requests, text)
	}
	for _, forbidden := range []string{"auth_ref", "internal-account-ref", "internal-credential"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("CLI exposes access detail %q", forbidden)
		}
	}
}

func TestDownloadSubmitInclusiveDates(t *testing.T) {
	var request struct {
		Source     string   `json:"source"`
		Collection string   `json:"collection"`
		Start      string   `json:"start"`
		End        string   `json:"end"`
		Band       int      `json:"band"`
		Times      []string `json:"times"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/downloads" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"download":{"id":42,"source_id":"goes18","collection_id":"goes18:ABI-L1b-RadF","state":"pending"}}`))
	}))
	defer server.Close()
	c := &client{base: server.URL, http: &http.Client{Timeout: time.Second}}
	if err := c.downloadSubmit([]string{"--source", "goes18", "--collection", "goes18:ABI-L1b-RadF", "--start", "2023-06-01", "--end", "2023-06-30", "--band", "6", "--hourly"}); err != nil {
		t.Fatal(err)
	}
	if request.Start != "2023-06-01" || request.End != "2023-07-01" || request.Band != 6 || len(request.Times) != 24 || request.Times[0] != "00:00" || request.Times[23] != "23:00" {
		t.Fatalf("request=%+v", request)
	}
	if err := c.downloadSubmit([]string{"--source", "goes18", "--collection", "goes18:ABI-L1b-RadF", "--start", "2023-06-30", "--end", "2023-06-01"}); err == nil {
		t.Fatal("accepted reversed dates")
	}
}
