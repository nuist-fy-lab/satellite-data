package downloadcoord

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/cloudsat"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type cloudOrbitTransport func(*http.Request) (*http.Response, error)

func (f cloudOrbitTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cloudOrbitMetadata []byte

func (m cloudOrbitMetadata) Fetch(context.Context, string, int64) ([]byte, error) { return m, nil }

func TestCloudSatOrbitProviderMetadataCoordinator(t *testing.T) {
	var data bytes.Buffer
	fmt.Fprint(&data, "Dataset {\n Float32 /2B-CLDCLASS/Geolocation_Fields/Profile_time[nray = 2];\n Float32 /2B-CLDCLASS/Geolocation_Fields/UTC_start;\n} sample;\n\nData:\n")
	for _, v := range []uint32{2, 2} {
		_ = binary.Write(&data, binary.BigEndian, v)
	}
	for _, v := range []float32{0, 5932.799, 83568.914} {
		_ = binary.Write(&data, binary.BigEndian, v)
	}
	name := "2006365231248_03606_CS_2B-CLDCLASS_GRANULE_P1_R05_E02_F00.hdf"
	client := &http.Client{Transport: cloudOrbitTransport(func(r *http.Request) (*http.Response, error) {
		body := `<catalog><dataset/></catalog>`
		if strings.Contains(r.URL.Path, "/2006/365/") {
			body = `<catalog><dataset><dataset name="` + name + `" urlPath="cloudsat-data/2B-CLDCLASS.P1_R05/2006/365/` + name + `"/></dataset></catalog>`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p := cloudsat.NewWithMetadata(client, cloudOrbitMetadata(data.Bytes()))
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const key = "cloudsat:2B-CLDCLASS.P1_R05"
	if err := s.UpsertProducts(t.Context(), "cloudsat", []model.Product{{ProductKey: key, SourceID: "cloudsat", AreaType: "ORBIT"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2007, 1, 1, 0, 0, 0, 0, time.UTC)
	req, err := s.EnqueueDownloadRequest(t.Context(), "cloudsat", "cloudsat", key, start, start.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dl, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, dl)
	if err != nil {
		t.Fatal(err)
	}
	for req.Cursor.Before(req.End) {
		if err := c.advanceDiscovery(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		req, err = s.DownloadRequest(t.Context(), req.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 1 || (*specs)[0].GranuleID != "cloudsat:"+name {
		t.Fatalf("got %+v", *specs)
	}
}
