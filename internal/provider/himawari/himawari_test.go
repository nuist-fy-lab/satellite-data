package himawari

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
				if calls == 1 && r.URL.Query().Get("start-after") != prefix {
					t.Errorf("start-after=%q", r.URL.Query().Get("start-after"))
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
			start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + tc.product, Start: start, End: start.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 4 || calls != 2 {
				t.Fatalf("granules=%+v calls=%d", got, calls)
			}
			for i, g := range got {
				code := fmt.Sprintf("%s%02d", tc.code, i+1)
				if g.ID != "himawari9:"+tc.product+":20240101T0000Z:"+code || len(g.Assets) != 2 || g.Metadata["observation_code"] != code || g.Metadata["time_precision"] != "timeline" || !g.End.IsZero() || !g.Start.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
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
		if got := r.URL.Query().Get("start-after"); got != base+"0000/" {
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
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: start, End: start.Add(10 * time.Minute)})
	if err != nil || len(got) != 1 || len(got[0].Assets) != 2 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
}

func TestRegionalTimelineBeforeRequestIsNotCoverage(t *testing.T) {
	product := "AHI-L1b-Japan"
	object := product + "/2024/01/01/0000/HS_H09_20240101_0000_B01_JP02_R10_S0101.DAT.bz2"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "<ListBucketResult><Contents><Key>" + object + "</Key></Contents></ListBucketResult>"
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, _ := New(9, objectstore.NewClient(client))
	anchor := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	request := anchor.Add(3 * time.Minute)
	end := anchor.Add(4 * time.Minute)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: anchor, End: end})
	if err != nil || len(got) != 1 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
	if producttime.OwnsCoverage(got[0].Start, got[0].End, request, request, end) {
		t.Fatal("unknown observation end cannot establish ownership at a later request start")
	}
	got, err = p.Search(context.Background(), model.Query{CollectionID: "himawari9:" + product, Start: request, End: end})
	if err != nil || len(got) != 0 {
		t.Fatalf("granules=%+v err=%v", got, err)
	}
}
