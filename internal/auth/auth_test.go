package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestEarthCAREOfflineTokenExchange(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != earthcareTokenEndpoint {
			t.Fatalf("unexpected token endpoint %s", req.URL)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("grant_type") != "refresh_token" || values.Get("refresh_token") != "offline-value" || values.Get("client_id") != earthcareClientID {
			t.Fatalf("unexpected form %v", values)
		}
		payload, _ := json.Marshal(map[string]any{"access_token": "short-access", "expires_in": 300})
		return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(string(payload))), Header: make(http.Header)}, nil
	})}
	a := &earthcareToken{client: client, offline: "offline-value"}
	req, _ := http.NewRequest(http.MethodGet, "https://catalog.maap.eo.esa.int/data/example.h5", nil)
	if err := a.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer short-access" {
		t.Fatalf("authorization=%q", got)
	}
}
