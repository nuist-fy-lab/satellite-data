package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestPortalRoutesPreActivateTargetView(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := New(provider.NewRegistry(), store)

	cases := []struct {
		path string
		view string
		wide bool
	}{
		{"/", "calendar-view", true},
		{"/calendar", "calendar-view", true},
		{"/downloads", "downloads-view", false},
		{"/era5", "era5-view", false},
		{"/catalog", "catalog-view", false},
		{"/monitor", "monitor-view", false},
		{"/access", "access-view", false},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", tc.path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-view="`+tc.view+`"`) {
			t.Fatalf("GET %s missing data-view=%s", tc.path, tc.view)
		}
		if !strings.Contains(body, `id="calendar-view"`) {
			t.Fatalf("GET %s missing id=calendar-view", tc.path)
		}
		if !strings.Contains(body, `<section class="view active" id="`+tc.view+`">`) {
			t.Fatalf("GET %s did not activate section %s", tc.path, tc.view)
		}
		if tc.view != "calendar-view" && strings.Contains(body, `<section class="view active" id="calendar-view">`) {
			t.Fatalf("GET %s left calendar-view active", tc.path)
		}
		hasWide := strings.Contains(body, `<main class="calendar-wide">`)
		if hasWide != tc.wide {
			t.Fatalf("GET %s calendar-wide=%v, want %v", tc.path, hasWide, tc.wide)
		}
	}
}

func TestDownloadTasksCursorAPIAndSSEStream(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := authstore.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	user, err := auth.CreateUser(t.Context(), "alice", "test-password-1234", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := auth.CreateAPIKey(t.Context(), user.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProducts(t.Context(), "modis", []model.Product{{
		ProductKey: "modis:sample", SourceID: "modis", Satellite: "Terra", Instrument: "MODIS", DataType: "MOD06_L2", DisplayName: "Cloud",
	}}); err != nil {
		t.Fatal(err)
	}

	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, store, nil, auth, time.Hour)
	server := httptest.NewServer(handler)
	defer server.Close()

	for i := 0; i < 3; i++ {
		body := `{"products":["modis:sample"],"start":"2026-01-0` + string(rune('1'+i)) + `","end":"2026-01-0` + string(rune('2'+i)) + `"}`
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/download-tasks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("create task %d status=%d", i, resp.StatusCode)
		}
	}

	req1, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/download-tasks?cursor=&limit=2", nil)
	req1.Header.Set("Authorization", "Bearer "+key)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	var page1 struct {
		Tasks      []downloadTaskView `json:"tasks"`
		Total      int                `json:"total"`
		PageSize   int                `json:"page_size"`
		HasMore    bool               `json:"has_more"`
		NextCursor string             `json:"next_cursor"`
		Cursor     string             `json:"cursor"`
	}
	if err := json.NewDecoder(resp1.Body).Decode(&page1); err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()
	if page1.Total != 3 || len(page1.Tasks) != 2 || page1.PageSize != 2 || !page1.HasMore || page1.NextCursor == "" || page1.Cursor == "" {
		t.Fatalf("unexpected cursor page1: %+v", page1)
	}

	req2, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/download-tasks?cursor="+page1.NextCursor+"&limit=2", nil)
	req2.Header.Set("Authorization", "Bearer "+key)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	var page2 struct {
		Tasks   []downloadTaskView `json:"tasks"`
		HasMore bool               `json:"has_more"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&page2); err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if len(page2.Tasks) != 1 || page2.HasMore || page2.Tasks[0].Key == page1.Tasks[0].Key || page2.Tasks[0].Key == page1.Tasks[1].Key {
		t.Fatalf("unexpected cursor page2: %+v", page2)
	}

	// Verify SSE stream sends initial tasks event, and sync when since matches cursor.
	for _, tc := range []struct {
		urlSuffix string
		wantEvent string
	}{
		{"?limit=3", "tasks"},
		{"?limit=2&since=" + page1.Cursor, "sync"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		sreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/download-tasks/stream"+tc.urlSuffix, nil)
		sreq.Header.Set("Authorization", "Bearer "+key)
		sresp, err := http.DefaultClient.Do(sreq)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		ev, _ := readNextSSEEvent(t, sresp)
		sresp.Body.Close()
		cancel()
		if ev != tc.wantEvent {
			t.Fatalf("stream %s got event %q, want %q", tc.urlSuffix, ev, tc.wantEvent)
		}
	}
}

func TestNDJSONCatalogAndStreams(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := authstore.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	adminUser, err := auth.CreateUser(t.Context(), "admin", "test-password-1234", "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := auth.CreateUser(t.Context(), "alice", "test-password-1234", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, userKey, err := auth.CreateAPIKey(t.Context(), user.ID, "test")
	if err != nil {
		t.Fatal(err)
	}

	products := []model.Product{
		{ProductKey: "goes18:ABI-L1b-RadC", SourceID: "goes18", Satellite: "GOES-18", Instrument: "ABI", DataType: "ABI-L1b-RadC", DisplayName: "CONUS"},
		{ProductKey: "goes18:GLM-L2-LCFA", SourceID: "goes18", Satellite: "GOES-18", Instrument: "GLM", DataType: "GLM-L2-LCFA", DisplayName: "Lightning"},
	}
	if err := store.UpsertProducts(t.Context(), "goes18", products); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SaveOfficialBatch(t.Context(), state.WorkBatch{Product: "goes18:GLM-L2-LCFA", Days: []string{"2024-07-01"}}, []model.Granule{{
		ID: "2024-07-01", Start: day, Assets: []model.Asset{{Name: "a.nc", Size: 10}},
	}}); err != nil {
		t.Fatal(err)
	}

	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	centralSvc := central.NewService(nil)
	handler := NewWithAuth(registry, store, nil, auth, time.Hour, centralSvc)
	server := httptest.NewServer(handler)
	defer server.Close()

	// NDJSON products & download catalog streaming.
	for _, endpoint := range []string{"/api/products?source=goes18&stream=1", "/api/v1/downloads/catalog?source=goes18&stream=1"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+userKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
			t.Fatalf("%s content-type=%q", endpoint, ct)
		}
		scanner := bufio.NewScanner(resp.Body)
		lines := 0
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) != "" {
				lines++
			}
		}
		resp.Body.Close()
		if lines != 2 {
			t.Fatalf("%s got %d ndjson lines, want 2", endpoint, lines)
		}
	}

	// index/stream cursor resume -> sync when unchanged, and monitor streams -> snapshot.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	idxReq1, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/index/stream?product=goes18:GLM-L2-LCFA&year=2024&watch=1", nil)
	idxReq1.Header.Set("Authorization", "Bearer "+userKey)
	idxResp1, err := http.DefaultClient.Do(idxReq1)
	if err != nil {
		t.Fatal(err)
	}
	ev, idxData := readNextNamedSSEEvent(t, idxResp1, "index")
	idxResp1.Body.Close()
	if ev != "index" {
		t.Fatalf("expected initial index event, got %q", ev)
	}
	var idxPayload struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(idxData, &idxPayload); err != nil || idxPayload.Cursor == "" {
		t.Fatalf("missing index cursor: %s", string(idxData))
	}

	idxReq2, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/index/stream?product=goes18:GLM-L2-LCFA&year=2024&cursor="+idxPayload.Cursor+"&watch=1", nil)
	idxReq2.Header.Set("Authorization", "Bearer "+userKey)
	idxResp2, err := http.DefaultClient.Do(idxReq2)
	if err != nil {
		t.Fatal(err)
	}
	evSync, _ := readNextNamedSSEEvent(t, idxResp2, "sync")
	idxResp2.Body.Close()
	if evSync != "sync" {
		t.Fatalf("expected sync event on cursor resume, got %q", evSync)
	}

	for _, streamPath := range []string{"/api/v1/monitor/downloads/stream?window=1h", "/api/v1/monitor/links/stream?window=1h"} {
		mreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+streamPath, nil)
		mreq.Header.Set("Authorization", "Bearer "+userKey)
		mresp, err := http.DefaultClient.Do(mreq)
		if err != nil {
			t.Fatal(err)
		}
		mev, _ := readNextSSEEvent(t, mresp)
		mresp.Body.Close()
		if mev != "snapshot" {
			t.Fatalf("%s initial event=%q, want snapshot", streamPath, mev)
		}
	}

	// Egress stream rejects non-admin and serves admin session.
	forbiddenReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/access/download-egress/stream", nil)
	forbiddenReq.Header.Set("Authorization", "Bearer "+userKey)
	forbiddenResp, err := http.DefaultClient.Do(forbiddenReq)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenResp.Body.Close()
	if forbiddenResp.StatusCode != http.StatusUnauthorized && forbiddenResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected non-admin rejection, got %d", forbiddenResp.StatusCode)
	}

	sessionToken, err := auth.CreateSession(t.Context(), adminUser.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	egReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/access/download-egress/stream", nil)
	egReq.AddCookie(&http.Cookie{Name: accessSessionCookie, Value: sessionToken})
	egResp, err := http.DefaultClient.Do(egReq)
	if err != nil {
		t.Fatal(err)
	}
	egEv, _ := readNextSSEEvent(t, egResp)
	egResp.Body.Close()
	if egEv != "egress" {
		t.Fatalf("admin egress stream initial event=%q, want egress", egEv)
	}
}

func readNextSSEEvent(t *testing.T, resp *http.Response) (string, json.RawMessage) {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	var event string
	var data json.RawMessage
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = json.RawMessage(strings.TrimPrefix(line, "data: "))
		} else if line == "" && event != "" {
			return event, data
		}
	}
	t.Fatalf("stream ended before event: %v", scanner.Err())
	return "", nil
}

func readNextNamedSSEEvent(t *testing.T, resp *http.Response, want string) (string, json.RawMessage) {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	var event string
	var data json.RawMessage
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = json.RawMessage(strings.TrimPrefix(line, "data: "))
		} else if line == "" && event != "" {
			if event == want {
				return event, data
			}
			event = ""
			data = nil
		}
	}
	t.Fatalf("stream ended before %q: %v", want, scanner.Err())
	return "", nil
}
