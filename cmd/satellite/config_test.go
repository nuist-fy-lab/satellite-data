package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureCLIKey = "satellite_v1_fixture_not-a-real-key"

func TestCLIConfigPrivateStorageAndOverrides(t *testing.T) {
	t.Setenv("SATELLITE_SERVER", "")
	t.Setenv("SATELLITE_API_KEY", "")
	path := filepath.Join(t.TempDir(), "cli", "config.json")
	if _, err := loadCLIConfig(path); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if err := saveCLIConfig(path, cliConfig{Server: "https://satellite.example/", APIKey: fixtureCLIKey}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config is not mode 0600")
	}
	cfg, err := loadCLIConfig(path)
	if err != nil || cfg.Server != "https://satellite.example" || cfg.APIKey != fixtureCLIKey {
		t.Fatal("config failed to round-trip")
	}
	t.Setenv("SATELLITE_SERVER", " https://override.example/ ")
	cfg, err = loadCLIConfig(path)
	if err != nil || cfg.Server != "https://override.example" {
		t.Fatal("server override not normalized")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCLIConfig(path); err == nil {
		t.Fatal("world-readable config accepted")
	}
}

func TestCLIConfigRejectsUnsafeServerAndProviderTokens(t *testing.T) {
	for _, server := range []string{"http://example.com", "http://localhost.example.com", "https://user:password@example.com", "https://example.com?token=fixture", "https://example.com#key", "ftp://example.com"} {
		cfg := cliConfig{Server: server, APIKey: fixtureCLIKey}
		if err := cfg.validate(); err == nil {
			t.Fatalf("unsafe centre URL accepted: %s", server)
		}
	}
	for _, server := range []string{"https://example.com", "http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"} {
		cfg := cliConfig{Server: server, APIKey: fixtureCLIKey}
		if err := cfg.validate(); err != nil {
			t.Fatalf("valid centre URL rejected: %s", server)
		}
	}
	for _, key := range []string{"", "fixture-provider-token", "nsmc_v1_fixture", fixtureCLIKey + "\nextra"} {
		cfg := cliConfig{Server: "https://example.com", APIKey: key}
		if err := cfg.validate(); err == nil {
			t.Fatal("non-Satellite CLI key accepted")
		}
	}
	if err := runConfigSet([]string{"--api-key", "fixture"}); err == nil {
		t.Fatal("API key command-line argument accepted")
	}
}

func TestCLIRequestsUseCentreBearerKeyWithoutRedirecting(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	centre := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureCLIKey || len(r.Cookies()) != 0 {
			t.Error("CLI did not use only the centre API key")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sources":[]}`))
	}))
	defer centre.Close()
	c := newClient(cliConfig{Server: centre.URL, APIKey: fixtureCLIKey})
	if err := c.sourceList(); err != nil {
		t.Fatal(err)
	}
	if err := c.get("/redirect", nil); err == nil || redirected {
		t.Fatal("redirect was followed or accepted")
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"` + fixtureCLIKey + `"}`)), Header: make(http.Header)}, nil
}

func TestCLIErrorsDoNotEchoAPIKey(t *testing.T) {
	c := newClient(cliConfig{Server: "https://satellite.example", APIKey: fixtureCLIKey})
	c.http.Transport = errorTransport{}
	err := c.get("/api/v1/sources", nil)
	if err == nil || strings.Contains(err.Error(), fixtureCLIKey) {
		t.Fatal("error handling failed or echoed the API key")
	}
}
