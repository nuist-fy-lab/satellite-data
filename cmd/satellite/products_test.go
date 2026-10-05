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

func TestProductKeysAndDataSelectionForEverySource(t *testing.T) {
	products := []string{"goes16:GLM-L2-LCFA", "goes17:GLM-L2-LCFA", "goes18:ABI-L1b-RadF", "goes19:ABI-L1b-RadF", "himawari8:AHI-L1b-FLDK", "himawari9:AHI-L1b-FLDK", "modis:C123-LAADS", "viirs:C456-LAADS", "mtg:EO:EUM:DAT:0678", "earthcare:ATL_NOM_1B", "calipso:C789-ASDC", "cloudsat:2B-GEOPROF_R05", "era5:reanalysis-era5-single-levels"}
	for _, product := range products {
		t.Run(product, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/downloads" {
					t.Errorf("CLI started scheduling instead of sending one order: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Source, Collection, Start, End string
					Inputs                         map[string]json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Collection != product || body.Start != "2024-01-01" || body.End != "2024-01-02" {
					t.Errorf("wrong selection payload: %+v", body)
				}
				if strings.HasPrefix(product, "era5:") && string(body.Inputs["variable"]) != `["2m_temperature"]` {
					t.Error("missing on-demand selection")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"download": downloadRequest{ID: 1, CollectionID: product, SourceID: body.Source, State: "pending"}})
			}))
			defer server.Close()
			c := &client{base: server.URL, http: &http.Client{Timeout: time.Second}}
			args := []string{"--product", product, "--day", "2024-01-01"}
			if strings.HasPrefix(product, "era5:") {
				args = append(args, "--variable", "2m_temperature")
			}
			if err := c.downloadSubmit(args); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("one order required %d requests", calls)
			}
		})
	}
	if _, _, err := resolveProduct("modis", "", "viirs:C456-LAADS"); err == nil {
		t.Fatal("accepted source mismatch")
	}
}

func TestQueryUsesOneCentreRequestAndOmitsTransferSecrets(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Query().Get("start") != "2024-01-01T00:00:00Z" || r.URL.Query().Get("end") != "2024-01-18T00:00:00Z" {
			t.Errorf("CLI split or changed the query: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"granules":[{"id":"g1","start":"2024-01-01T00:00:00Z","assets":[{"id":"a1","name":"result.nc","url":"https://cdn.example/?signature=private","auth_ref":"internal-credential"}]}]}`)
	}))
	defer server.Close()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	previous := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = previous }()
	c := &client{base: server.URL, http: &http.Client{Timeout: time.Second}}
	if err := c.queryProducts([]string{"--product", "modis:C123-LAADS", "--start", "2024-01-01", "--end", "2024-01-18", "--json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Contains(string(data), "signature=private") || strings.Contains(string(data), "internal-credential") {
		t.Fatalf("unsafe thin CLI query: %d %s", calls, data)
	}
}
