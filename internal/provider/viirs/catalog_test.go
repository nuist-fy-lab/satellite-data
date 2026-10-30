package viirs

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogOnlyOffersVersionsWithOfficialGranules(t *testing.T) {
	p := New(&http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		for key, want := range map[string]string{"instrument": "VIIRS", "has_granules": "true", "include_has_granules": "true", "include_granule_counts": "true"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s=%q want %s", key, got, want)
			}
		}
		body := `{"feed":{"entry":[
		 {"id":"C-old","short_name":"VNP02","version_id":"1","platforms":["Suomi-NPP"],"has_granules":false,"granule_count":0},
		 {"id":"C-current","short_name":"VNP02","version_id":"2","platforms":["Suomi-NPP"],"has_granules":true,"granule_count":40}
		]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	collections, err := p.Collections(context.Background())
	if err != nil || len(collections) != 1 || collections[0].Metadata["version"] != "2" || collections[0].Metadata["product_count"] != "40" {
		t.Fatalf("%+v %v", collections, err)
	}
}

func TestCatalogRejectsMissingAvailabilityEvidence(t *testing.T) {
	p := New(&http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"feed":{"entry":[{"id":"C1","short_name":"VNP02","version_id":"2","platforms":["Suomi-NPP"]}]}}`))}, nil
	})})
	if _, err := p.Collections(context.Background()); err == nil {
		t.Fatal("unconfirmed version published")
	}
}
