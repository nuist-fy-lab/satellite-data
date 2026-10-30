package laads

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogIncludesOnlyVersionsWithOfficialGranules(t *testing.T) {
	calls := 0
	p := New(&http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		for key, want := range map[string]string{"provider": "LAADS", "instrument": "MODIS", "has_granules": "true", "include_has_granules": "true", "include_granule_counts": "true"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("%s=%q want %s", key, got, want)
			}
		}
		body := `{"feed":{"entry":[
		 {"id":"C-empty","short_name":"MOD09","version_id":"6","platforms":["Terra"],"has_granules":false,"granule_count":0},
		 {"id":"C-current","short_name":"MOD03","version_id":"6.1","platforms":["Terra"],"has_granules":true,"granule_count":20},
		 {"id":"C-new","short_name":"MOD03","version_id":"7","platforms":["Terra"],"has_granules":true,"granule_count":12},
		 {"id":"C-derived","short_name":"CLDPROP","version_id":"1.1","platforms":["Aqua"],"has_granules":true,"granule_count":8},
		 {"id":"C-zero","short_name":"MOD99","version_id":"7","platforms":["Terra"],"has_granules":true,"granule_count":0}
		]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	collections, err := p.Collections(context.Background())
	if err != nil || len(collections) != 3 {
		t.Fatalf("collections=%+v err=%v", collections, err)
	}
	versions := map[string]bool{}
	for _, c := range collections {
		versions[c.Metadata["version"]] = true
		product := provider.ProductFromCollection(p.Source(), c)
		if product.ProductCount < 1 || product.Metadata["has_granules"] != "true" {
			t.Fatalf("missing existence evidence: %+v", product)
		}
	}
	if versions["6"] || !versions["6.1"] || !versions["7"] || !versions["1.1"] {
		t.Fatalf("incorrect version policy: %v", versions)
	}
	if _, err := p.Collections(context.Background()); err != nil || calls != 1 {
		t.Fatalf("catalog cache: calls=%d err=%v", calls, err)
	}
}

func TestCatalogMissingAvailabilityFailsInsteadOfPublishingEmptyCatalog(t *testing.T) {
	p := New(&http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"feed":{"entry":[{"id":"C1","short_name":"MOD03","version_id":"6.1","platforms":["Terra"]}]}}`))}, nil
	})})
	if _, err := p.Collections(context.Background()); err == nil {
		t.Fatal("unconfirmed version published")
	}
}
