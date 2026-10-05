package central

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

func TestCDSFetchUsesBoundIdentityAndNeverForwardsProviderToken(t *testing.T) {
	s := testStore(t)
	_, _ = addAccount(t, s, "era5", "cds", Secret{Token: "fixture-cds-token"})
	assetRequests := 0
	host := "object-store.os-api.cci2.ecmwf.int"
	service := NewService(s, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cds.climate.copernicus.eu" {
			if strings.Contains(r.URL.Path, "/results") {
				if r.Header.Get("PRIVATE-TOKEN") != "fixture-cds-token" {
					t.Error("result lookup lost the bound credential")
				}
				return reply(http.StatusOK, `{"asset":{"value":{"href":"https://`+host+`/cache/result.nc?signature=fixture","file:size":128,"type":"application/x-netcdf"}}}`), nil
			}
			if r.Method == http.MethodPost {
				return reply(http.StatusAccepted, `{"jobID":"job-1","status":"accepted"}`), nil
			}
			return reply(http.StatusOK, `{"id":"reanalysis-era5-single-levels","inputs":{"variable":{}}}`), nil
		}
		assetRequests++
		if r.URL.Hostname() != "object-store.os-api.cci2.ecmwf.int" || r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("provider credential reached asset storage")
		}
		if r.Header.Get("Range") != "bytes=0-3" {
			t.Error("Range not forwarded")
		}
		response := reply(http.StatusPartialContent, "CDF1")
		response.Header.Set("Content-Range", "bytes 0-3/128")
		return response, nil
	})})
	if _, err := service.Submit(t.Context(), era5.Submission{CollectionID: "era5:reanalysis-era5-single-levels", Inputs: map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)}}); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/fetch", strings.NewReader(`{"source_id":"era5","url":"https://cds.climate.copernicus.eu/api/retrieve/v1/jobs/job-1/download","range":"bytes=0-3"}`))
		w := httptest.NewRecorder()
		service.Gateway().ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != http.StatusPartialContent || w.Body.String() != "CDF1" || assetRequests != 1 {
		t.Fatalf("bound CDS fetch: %d %s", w.Code, w.Body.String())
	}
	host = "127.0.0.1"
	w = request()
	if w.Code != http.StatusBadGateway || assetRequests != 1 || strings.Contains(w.Body.String(), "fixture-cds-token") {
		t.Fatal("unsafe CDS asset host was fetched or exposed")
	}
}
