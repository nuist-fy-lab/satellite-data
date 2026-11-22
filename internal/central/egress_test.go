package central

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/egress"
)

func TestConfiguredDownloadEgressOverridesOnlyFetchRoute(t *testing.T) {
	store := testStore(t)
	_, _ = addAccount(t, store, "modis", "laads", Secret{Token: "fixture-laads"})
	s := NewService(store)
	var seen []string
	s.clientFactory = func(u *url.URL) *http.Client {
		if u == nil {
			seen = append(seen, "direct")
		} else {
			seen = append(seen, u.String())
		}
		return &http.Client{}
	}
	s.egress = egress.NewPool(&egress.Config{Routes: []egress.Route{
		{ID: "wan1", Proxy: "socks5://127.0.0.1:13881", PublicIP: "58.212.61.218"},
		{ID: "wan2", Proxy: "socks5://127.0.0.1:13882", PublicIP: "58.212.60.213"},
	}})
	first, release, err := s.egress.Acquire(context.Background(), "modis")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := url.Parse(first.Proxy)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.resolveFetch(context.Background(), "modis", nil, proxy, laadsArchiveHost)
	if err != nil {
		t.Fatal(err)
	}
	if selected.release != nil {
		selected.release()
	}
	release()
	ordinary, err := s.resolve(context.Background(), "modis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.release != nil {
		ordinary.release()
	}
	if len(seen) != 2 || seen[0] != first.Proxy || seen[1] != "direct" {
		t.Fatalf("download route must not override control route: %v", seen)
	}
}
