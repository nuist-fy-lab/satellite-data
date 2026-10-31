package downloadcoord

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/himawari"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type himawariRoundTrip func(*http.Request) (*http.Response, error)

func (f himawariRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHimawariObservedIntervalOwnedAtRequestStart(t *testing.T) {
	const encoded = "QlpoOTFBWSZTWZkBFX4AAA5/j3G4AnAAA3wgfVJcEKAyEaBAEEAAAAgAAiAAcjPSAaYgMjQAABiEEao9JoNpPaoZGRo8p7UQ0IcoYGP0qCHYQGQDAhr99up1c2K+Xo243BtFWLhNsWeHnTqFSAKSfGiJ/hm6p2eJqkmslkDd5XJkkEdPXW0Ez+KL7gpgctlBa8mED8XckU4UJCZARV+A"
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	const product = "AHI-L1b-Japan"
	const key = "AHI-L1b-Japan/2024/01/01/0000/HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT.bz2"
	httpClient := &http.Client{Transport: himawariRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("list-type") != "" {
			body := "<ListBucketResult><Contents><Key>" + key + "</Key></Contents></ListBucketResult>"
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if r.Header.Get("Range") == "" {
			t.Error("missing range")
		}
		header := make(http.Header)
		header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(compressed)-1, len(compressed)))
		return &http.Response{StatusCode: 206, Header: header, ContentLength: int64(len(compressed)), Body: io.NopCloser(bytes.NewReader(compressed))}, nil
	})}
	p, err := himawari.New(9, objectstore.NewClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	collection := "himawari9:" + product
	if err := s.UpsertProducts(t.Context(), "himawari9", []model.Product{{ProductKey: collection, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 1, 0, 0, time.UTC)
	request, err := s.EnqueueDownloadRequest(t.Context(), "himawari9", "himawari9", collection, start, start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	client, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(*specs) != 1 || (*specs)[0].GranuleID != "himawari9:AHI-L1b-Japan:20240101T0000Z:JP01" {
		mu.Unlock()
		t.Fatalf("specs=%+v", *specs)
	}
	mu.Unlock()
	end := time.Date(2024, 1, 1, 0, 1, 49, 839251591, time.UTC)
	request, err = s.EnqueueDownloadRequest(t.Context(), "himawari9", "himawari9", collection, end, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 1 {
		t.Fatalf("end-exclusive boundary scheduled another asset: %+v", *specs)
	}
}

type regionalSpy struct {
	queries  []model.Query
	granules []model.Granule
	err      error
}

func (*regionalSpy) Source() model.Source                                    { return model.Source{ID: "himawari9"} }
func (*regionalSpy) Collections(context.Context) ([]model.Collection, error) { return nil, nil }
func (p *regionalSpy) Search(_ context.Context, q model.Query) ([]model.Granule, error) {
	p.queries = append(p.queries, q)
	return p.granules, p.err
}

func TestRegionalHeaderErrorDoesNotAdvance(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	collection := "himawari9:AHI-L1b-Japan"
	if err := s.UpsertProducts(t.Context(), "himawari9", []model.Product{{ProductKey: collection, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 3, 0, 0, time.UTC)
	request, err := s.EnqueueDownloadRequest(t.Context(), "himawari9", "himawari9", collection, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("incomplete header")
	p := &regionalSpy{err: failure}
	client, _, _ := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), request); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	current, err := s.DownloadRequest(t.Context(), request.ID)
	if err != nil || !current.Cursor.Equal(start) {
		t.Fatalf("request=%+v err=%v", current, err)
	}
}

func TestRegionalCoverageChunksAlignToUTC(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	collection := "himawari9:AHI-L1b-Target"
	if err := s.UpsertProducts(t.Context(), "himawari9", []model.Product{{ProductKey: collection, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	request, err := s.EnqueueDownloadRequest(t.Context(), "himawari9", "himawari9", collection, day.Add(3*time.Minute), day.Add(21*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	p := &regionalSpy{}
	client, _, _ := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	for request.Cursor.Before(request.End) {
		if err := c.advanceDiscovery(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		request, err = s.DownloadRequest(t.Context(), request.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(p.queries) != 3 {
		t.Fatalf("queries=%+v", p.queries)
	}
	for i, bounds := range [][2]time.Duration{{3 * time.Minute, 10 * time.Minute}, {10 * time.Minute, 20 * time.Minute}, {20 * time.Minute, 21 * time.Minute}} {
		if !p.queries[i].Start.Equal(day.Add(bounds[0])) || !p.queries[i].End.Equal(day.Add(bounds[1])) {
			t.Fatalf("query %d=%+v", i, p.queries[i])
		}
	}
}

func TestSceneInWindowUsesUTCSecondsAndExclusiveEnd(t *testing.T) {
	day := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	if !sceneInWindow([]string{"23:59:59"}, day, day.Add(23*time.Hour+50*time.Minute), day.AddDate(0, 0, 1)) {
		t.Fatal("last second of day omitted")
	}
	if sceneInWindow([]string{"00:10:00"}, day, day, day.Add(10*time.Minute)) {
		t.Fatal("exclusive end included")
	}
}

func TestRegionalSceneChunksKeepSecondsAndSkipOtherWindows(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	collection := "himawari9:AHI-L1b-Japan"
	if err := s.UpsertProducts(t.Context(), "himawari9", []model.Product{{ProductKey: collection, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	granule := model.Granule{ID: "jp", Start: day.Add(12*time.Minute + 21*time.Second), End: day.Add(13 * time.Minute), Assets: []model.Asset{{ID: "asset", Name: "x.dat", URL: "https://example.test/x"}}}
	p := &regionalSpy{granules: []model.Granule{granule}}
	group, _, err := s.EnqueueDownloadGroup(t.Context(), "local", day, day.Add(21*time.Minute), []state.GroupProduct{{Source: "himawari9", Provider: "himawari9", Collection: collection, Selection: downloadfilter.Selection{Scenes: map[string][]string{"2024-01-01": {"00:12:20"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	client, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	request := group.Downloads[0]
	request.Cursor = day.Add(3 * time.Minute)
	for request.Cursor.Before(request.End) {
		if err := c.advanceDiscovery(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		request, err = s.DownloadRequest(t.Context(), request.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(p.queries) != 1 || !p.queries[0].Start.Equal(day.Add(9*time.Minute)) || !p.queries[0].End.Equal(day.Add(21*time.Minute)) {
		t.Fatalf("queries=%+v", p.queries)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 1 || (*specs)[0].GranuleID != "jp" {
		t.Fatalf("specs=%+v", *specs)
	}
}
