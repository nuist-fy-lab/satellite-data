package central

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type metadataRoundTrip func(*http.Request) (*http.Response, error)

func (f metadataRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudSatMetadataClientLimitAndStatus(t *testing.T) {
	status := http.StatusOK
	c := &CloudSatMetadataClient{client: &http.Client{Transport: metadataRoundTrip(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Source string `json:"source_id"`
			URL    string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Source != "cloudsat" || payload.URL != "https://example.test/data" {
			t.Fatalf("request %#v %v", payload, err)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("12345"))}, nil
	})}}
	if _, err := c.Fetch(t.Context(), "https://example.test/data", 4); err == nil {
		t.Fatal("oversized response accepted")
	}
	if data, err := c.Fetch(t.Context(), "https://example.test/data", 5); err != nil || string(data) != "12345" {
		t.Fatalf("got %q %v", data, err)
	}
	status = 401
	if _, err := c.Fetch(t.Context(), "https://example.test/data", 5); err == nil {
		t.Fatal("HTTP failure accepted")
	}
}
