package cloudsat

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func orbitFixture(product string, utc float32, values ...float32) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Dataset {\n    Float32 /%s/Geolocation_Fields/Profile_time[nray = %d];\n    Float32 /%s/Geolocation_Fields/UTC_start;\n} sample;\n\nData:\n", product, len(values), product)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(values)))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(values)))
	for _, v := range values {
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	_ = binary.Write(&b, binary.BigEndian, utc)
	return b.Bytes()
}

func TestOrbitDODSValidation(t *testing.T) {
	base := time.Date(2006, 12, 31, 23, 12, 48, 0, time.UTC)
	data := orbitFixture("2B-CLDCLASS", 83568.914, 0, 5932.799, 300)
	start, end, last, err := parseOrbitDODS(data, "2B-CLDCLASS", base)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2006, 12, 31, 0, 0, 0, 0, time.UTC).Add(time.Duration(math.Round(float64(float32(83568.914)) * 1e9)))
	wantLast := wantStart.Add(time.Duration(math.Round(float64(float32(5932.799)) * 1e9)))
	if !start.Equal(wantStart) || !last.Equal(wantLast) || !end.Equal(last.Add(time.Nanosecond)) || last.Year() != 2007 {
		t.Fatalf("start %s end %s last %s", start, end, last)
	}
	for _, bad := range [][]byte{data[:len(data)-1], orbitFixture("2B-CLDCLASS", 83568.914), orbitFixture("2B-CLDCLASS", 83568.914, 1), orbitFixture("2B-CLDCLASS", 83568.914, 0, 6001), orbitFixture("2B-CLDCLASS", 83568.914, 0, float32(math.NaN())), orbitFixture("2B-CLDCLASS", 83570, 0, 1)} {
		if _, _, _, err := parseOrbitDODS(bad, "2B-CLDCLASS", base); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}

type orbitReader struct {
	data  []byte
	calls int
	err   error
}

func (r *orbitReader) Fetch(ctx context.Context, _ string, _ int64) ([]byte, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.data, r.err
}

func TestOrbitSearchCrossYearAndExclusiveEnd(t *testing.T) {
	name := "2006365231248_03606_CS_2B-CLDCLASS_GRANULE_P1_R05_E02_F00.hdf"
	reader := &orbitReader{data: orbitFixture("2B-CLDCLASS", 83568.914, 0, 5932.799)}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<catalog><dataset/></catalog>`
		if strings.Contains(r.URL.Path, "/2006/365/") {
			body = `<catalog><dataset><dataset name="` + name + `" urlPath="cloudsat-data/2B-CLDCLASS.P1_R05/2006/365/` + name + `"/></dataset></catalog>`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p := NewWithMetadata(client, reader)
	_, end, last, err := parseOrbitDODS(reader.data, "2B-CLDCLASS", time.Date(2006, 12, 31, 23, 12, 48, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	q := model.Query{CollectionID: "cloudsat:2B-CLDCLASS.P1_R05", Start: last, End: last.Add(time.Second)}
	got, err := p.Search(context.Background(), q)
	if err != nil || len(got) != 1 || !got[0].End.Equal(end) || got[0].Metadata["coverage_basis"] != "profile_centres" || got[0].Metadata["last_profile_time"] != last.Format(time.RFC3339Nano) {
		t.Fatalf("got %#v %v", got, err)
	}
	q.Start = end
	q.End = end.Add(time.Second)
	got, err = p.Search(context.Background(), q)
	if err != nil || len(got) != 0 || reader.calls != 1 {
		t.Fatalf("exclusive got %#v err %v calls %d", got, err, reader.calls)
	}
	reader.err = errors.New("failure")
	p.cache = make(map[string]orbitCacheEntry)
	if _, err = p.Search(context.Background(), q); err == nil {
		t.Fatal("metadata error swallowed")
	}
}
