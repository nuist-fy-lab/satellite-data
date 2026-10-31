package himawari

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func useSampleHeader(t *testing.T, p *Provider) {
	t.Helper()
	p.openObservation = func(_ context.Context, asset model.Asset) (io.ReadCloser, error) {
		encoded := identityFixtures[strings.TrimSuffix(asset.Name, ".bz2")]
		if encoded == "" {
			return nil, fmt.Errorf("missing matching fixture for %s", asset.Name)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

func TestParseCycleFromObject(t *testing.T) {
	name := "AHI-L1b-FLDK/2026/09/22/0500/HS_H09_20260922_0500_B03_FLDK_R05_S0110.DAT.bz2"
	got, ok := parseCycleFromObject("AHI-L1b-FLDK", name)
	if !ok {
		t.Fatal("expected object path to parse")
	}
	want := time.Date(2026, 9, 22, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("cycle=%s want=%s", got, want)
	}
}

func TestSameUTCDay(t *testing.T) {
	a := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if !sameUTCDay(a, a.Add(23*time.Hour)) {
		t.Fatal("times on the same UTC day did not match")
	}
	if sameUTCDay(a, a.Add(24*time.Hour)) {
		t.Fatal("times on different UTC days matched")
	}
}

func TestHimawariCadence(t *testing.T) {
	if got := himawariCadence("AHI-L1b-FLDK"); got != "10m" {
		t.Fatalf("cadence=%q", got)
	}
	if got := himawariCadence("AHI-L1b-Target"); got != "event" {
		t.Fatalf("cadence=%q", got)
	}
}

func TestISatSSDoesNotInferEndFromCadence(t *testing.T) {
	for _, satellite := range []int{8, 9} {
		product := "AHI-L2-FLDK-ISatSS"
		name := product + "/2024/01/01/0000/example.nc"
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := "<ListBucketResult><Contents><Key>" + name + "</Key></Contents></ListBucketResult>"
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		p, _ := New(satellite, objectstore.NewClient(client))
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		got, err := p.Search(context.Background(), model.Query{CollectionID: fmt.Sprintf("himawari%d:%s", satellite, product), Start: start, End: start.Add(time.Minute)})
		if err != nil || len(got) != 1 || !got[0].End.IsZero() || got[0].Metadata["time_precision"] != "archive_minute" {
			t.Fatalf("satellite=%d granules=%+v err=%v", satellite, got, err)
		}
	}
}

func TestRegionalScenes(t *testing.T) {
	for _, tc := range []struct{ product, code string }{{"AHI-L1b-Japan", "JP"}, {"AHI-L1b-Target", "R3"}} {
		t.Run(tc.product, func(t *testing.T) {
			prefix := tc.product + "/2024/01/01/0000/"
			var objects []string
			for band := 1; band <= 2; band++ {
				for n := 1; n <= 4; n++ {
					objects = append(objects, fmt.Sprintf("%sHS_H09_20240101_0000_B%02d_%s%02d_R10_S0101.DAT.bz2", prefix, band, tc.code, n))
				}
			}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("prefix") != tc.product+"/2024/01/01/" {
					if r.URL.Query().Get("start-after") != tc.product+"/2023/12/31/2350/" {
						t.Errorf("start-after=%q", r.URL.Query().Get("start-after"))
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<ListBucketResult/>"))}, nil
				}
				items := objects[:4]
				next := "<IsTruncated>true</IsTruncated><NextContinuationToken>page2</NextContinuationToken>"
				if r.URL.Query().Get("continuation-token") != "" {
					items = append(objects[4:], objects[0])
					next = ""
				}
				body := "<ListBucketResult>" + next
				for _, name := range items {
					body += "<Contents><Key>" + name + "</Key></Contents>"
				}
				body += "</ListBucketResult>"
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			p, _ := New(9, objectstore.NewClient(client))
			useSampleHeader(t, p)
			start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + tc.product, Start: start, End: start.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 4 || calls != 3 {
				t.Fatalf("granules=%+v calls=%d", got, calls)
			}
			for i, g := range got {
				code := fmt.Sprintf("%s%02d", tc.code, i+1)
				if g.ID != "himawari9:"+tc.product+":20240101T0000Z:"+code || len(g.Assets) != 2 || g.Metadata["observation_code"] != code || g.Metadata["time_precision"] != "hsd_header_union" || g.End.IsZero() || g.Start.Second() != 13 {
					t.Fatalf("granule=%+v", g)
				}
			}
		})
	}
}

func TestRegionalObservationRejectsMismatchedNames(t *testing.T) {
	cycle := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{
		"HS_H09_20240101_0000_B01_JP05_R10_S0101.DAT.bz2",
		"HS_H09_20240101_0010_B01_JP01_R10_S0101.DAT.bz2",
		"HS_H09_20240101_0000_B01_R301_R10_S0101.DAT.bz2",
	} {
		if _, ok := regionalObservation("AHI-L1b-Japan", name, cycle); ok {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestRegionalSearchTimelineEndAndSegments(t *testing.T) {
	product := "AHI-L1b-Japan"
	base := product + "/2024/01/01/"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("prefix") != base {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<ListBucketResult/>"))}, nil
		}
		if got := r.URL.Query().Get("start-after"); got != base {
			t.Errorf("start-after=%q", got)
		}
		body := `<ListBucketResult>`
		for _, name := range []string{
			base + "0000/HS_H09_20240101_0000_B03_JP01_R05_S0102.DAT.bz2",
			base + "0000/HS_H09_20240101_0000_B03_JP01_R05_S0202.DAT.bz2",
			base + "0010/HS_H09_20240101_0010_B03_JP01_R05_S0101.DAT.bz2",
		} {
			body += "<Contents><Key>" + name + "</Key></Contents>"
		}
		body += `<IsTruncated>true</IsTruncated><NextContinuationToken>must-not-fetch</NextContinuationToken></ListBucketResult>`
		if r.URL.Query().Get("continuation-token") != "" {
			t.Fatal("listed past exclusive end")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	useSampleHeader(t, p)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: start, End: start.Add(10 * time.Minute)})
	if err != nil || len(got) != 1 || len(got[0].Assets) != 2 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
}

func TestRegionalTimelineBeforeRequestCoverage(t *testing.T) {
	product := "AHI-L1b-Japan"
	object := product + "/2024/01/01/0000/HS_H09_20240101_0000_B01_JP02_R10_S0101.DAT.bz2"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "<ListBucketResult><Contents><Key>" + object + "</Key></Contents></ListBucketResult>"
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	useSampleHeader(t, p)
	anchor := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	request := anchor.Add(time.Minute)
	end := anchor.Add(2 * time.Minute)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: anchor, End: end})
	if err != nil || len(got) != 1 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
	if !producttime.OwnsCoverage(got[0].Start, got[0].End, request, request, end) {
		t.Fatal("header interval should establish ownership at a later request start")
	}
	got, err = p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: request, End: end})
	if err != nil || len(got) != 1 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
}

func TestRegionalHeaderUnionAndCache(t *testing.T) {
	const one = "QlpoOTFBWSZTWZkBFX4AAA5/j3G4AnAAA3wgfVJcEKAyEaBAEEAAAAgAAiAAcjPSAaYgMjQAABiEEao9JoNpPaoZGRo8p7UQ0IcoYGP0qCHYQGQDAhr99up1c2K+Xo243BtFWLhNsWeHnTqFSAKSfGiJ/hm6p2eJqkmslkDd5XJkkEdPXW0Ez+KL7gpgctlBa8mED8XckU4UJCZARV+A"
	const two = "QlpoOTFBWSZTWVRBQkoAAA5/n/G4AHAEA3wgfFNeAKAiUaAEAAAQQAAACAACAAEgAHQzyTRqeo9AnpBhomnlHqekeFCshAA0aAAANABMybzh4LIg6IHECJEEc23HgpKpHBi9b1keBRdr9TDhloXkcGiHEbRYdk8QaawxPSVzkW50xbhzS5PORmotoKmD4IU+Eg+EqUMY+JICj+LuSKcKEgqIKElA"
	b1, _ := base64.StdEncoding.DecodeString(one)
	b2, _ := base64.StdEncoding.DecodeString(two)
	product := "AHI-L1b-Japan"
	path := product + "/2024/01/01/0000/HS_H09_20240101_0000_"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "<ListBucketResult>"
		for _, name := range []string{path + "B01_JP01_R10_S0101.DAT.bz2", path + "B02_JP01_R10_S0101.DAT.bz2"} {
			body += "<Contents><Key>" + name + "</Key><ETag>etag</ETag></Contents>"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body + "</ListBucketResult>"))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	var calls atomic.Int32
	p.openObservation = func(_ context.Context, a model.Asset) (io.ReadCloser, error) {
		calls.Add(1)
		data := b1
		if strings.Contains(a.Name, "_B02_") {
			data = b2
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	start := time.Date(2024, 1, 1, 0, 1, 50, 0, time.UTC)
	q := model.Query{CollectionID: "himawari9:" + product, Start: start, End: start.Add(time.Second)}
	for i := 0; i < 2; i++ {
		got, err := p.Search(context.Background(), q)
		if err != nil || len(got) != 1 {
			t.Fatalf("granules=%+v err=%v", got, err)
		}
		if got[0].Start.Second() != 13 || got[0].End.Sub(start) < 88*time.Millisecond || got[0].End.Sub(start) > 90*time.Millisecond {
			t.Fatalf("union=%+v", got[0])
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("header reads=%d, want 2", calls.Load())
	}
}

func TestRegionalHeaderHalfOpenBoundaries(t *testing.T) {
	product := "AHI-L1b-Japan"
	name := product + "/2024/01/01/0000/HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT.bz2"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "<ListBucketResult><Contents><Key>" + name + "</Key></Contents></ListBucketResult>"
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	useSampleHeader(t, p)
	header, _ := base64.StdEncoding.DecodeString(jp01BasicBlock)
	start, end, err := parseObservationHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []model.Query{
		{CollectionID: "himawari9:" + product, Start: start.Add(-time.Second), End: start},
		{CollectionID: "himawari9:" + product, Start: end, End: end.Add(time.Second)},
	} {
		got, err := p.Search(context.Background(), q)
		if err != nil || len(got) != 0 {
			t.Fatalf("query=%+v got=%+v err=%v", q, got, err)
		}
	}
}

func TestRegionalPreviousTimelineAcrossYear(t *testing.T) {
	product := "AHI-L1b-Japan"
	name := product + "/2023/12/31/2350/HS_H09_20231231_2350_B01_JP01_R10_S0101.DAT.bz2"
	var prefixes []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		prefix := r.URL.Query().Get("prefix")
		prefixes = append(prefixes, prefix)
		body := "<ListBucketResult>"
		if prefix == product+"/2023/12/31/" {
			body += "<Contents><Key>" + name + "</Key></Contents>"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body + "</ListBucketResult>"))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	useSampleHeader(t, p)
	start := time.Date(2024, 1, 1, 0, 0, 30, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: start, End: start.Add(time.Minute)})
	if err != nil || len(got) != 1 || got[0].ID != "himawari9:"+product+":20231231T2350Z:JP01" || len(prefixes) != 2 {
		t.Fatalf("granules=%+v prefixes=%v err=%v", got, prefixes, err)
	}
}
