package pipeline

import (
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/transfer"
)

func TestAllowedHostAcceptsNativeLAADS(t *testing.T) {
	for _, source := range []string{"modis", "viirs"} {
		if !allowedHost(source, "ladsweb.modaps.eosdis.nasa.gov") {
			t.Fatalf("%s should allow native LAADS archive", source)
		}
		if !allowedHost(source, "data.laadsdaac.earthdatacloud.nasa.gov") {
			t.Fatalf("%s should retain Earthdata Cloud compatibility", source)
		}
	}
	if allowedHost("modis", "example.invalid") {
		t.Fatal("unexpected host accepted")
	}
}

func TestERA5TasksUseOnlyTheStableCentreReference(t *testing.T) {
	if !allowedHost("era5", "cds.climate.copernicus.eu") {
		t.Fatal("stable CDS job reference was rejected")
	}
	for _, host := range []string{"object-store.os-api.cci2.ecmwf.int", "127.0.0.1", "example.invalid"} {
		if allowedHost("era5", host) {
			t.Fatalf("downloader accepted direct ERA5 asset host %s", host)
		}
	}
}

func TestProviderFlowAdaptsWithinCeiling(t *testing.T) {
	flow := &providerFlow{limit: 4, lastSample: time.Now().Add(-2 * time.Second)}
	for range 4 {
		if !flow.start() {
			t.Fatal("capacity lost before limit")
		}
	}
	if flow.start() {
		t.Fatal("flow exceeded concurrency limit")
	}
	flow.finish(false, true, 4)
	limit, active, _ := flow.snapshot()
	if limit != 2 || active != 3 {
		t.Fatalf("after throttle: limit=%d active=%d", limit, active)
	}
	for range 3 {
		flow.finish(true, false, 4)
	}
	for range 8 {
		if !flow.start() {
			t.Fatal("expected one active slot")
		}
		flow.finish(true, false, 4)
	}
	limit, _, _ = flow.snapshot()
	if limit != 3 {
		t.Fatalf("expected additive recovery, got %d", limit)
	}
}

func TestCongestionClassification(t *testing.T) {
	if !isCongestion(&transfer.HTTPError{Status: 429}) || !isCongestion(&transfer.HTTPError{Status: 503}) {
		t.Fatal("rate limiting and server errors should reduce concurrency")
	}
	if isCongestion(&transfer.HTTPError{Status: 404}) {
		t.Fatal("missing objects should not reduce concurrency")
	}
}
