package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
