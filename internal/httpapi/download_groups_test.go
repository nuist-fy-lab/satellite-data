package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestMultiProductSingleRequestAtomicDedupAndOwnerIsolation(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := authstore.Open(filepath.Join(t.TempDir(), "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	alice, err := auth.CreateUser(t.Context(), "alice", "test-password-1234", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := auth.CreateAPIKey(t.Context(), alice.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := auth.CreateUser(t.Context(), "bob", "test-password-1234", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := auth.CreateAPIKey(t.Context(), bob.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProducts(t.Context(), "modis", []model.Product{{ProductKey: "modis:sample", SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18"}}); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, store, nil, auth, time.Hour)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	bad := call(http.MethodPost, "/api/v1/downloads/batch", `{"products":["modis:sample","missing:product"],"start":"2023-05-03","end":"2023-05-04"}`, key)
	if bad.Code != http.StatusNotFound {
		t.Fatalf("invalid product submitted part of group: %d %s", bad.Code, bad.Body.String())
	}
	if orders, err := store.DownloadRequests(t.Context(), 10); err != nil || len(orders) != 0 {
		t.Fatalf("partial group created: %d %v", len(orders), err)
	}
	request := `{"products":["modis:sample","goes18:ABI-L1b-RadF"],"start":"2023-05-03","end":"2023-05-05"}`
	first := call(http.MethodPost, "/api/v1/downloads/batch", request, key)
	if first.Code != http.StatusAccepted {
		t.Fatalf("batch request failed: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Created bool                `json:"created"`
		Group   state.DownloadGroup `json:"group"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || !result.Created || result.Group.Products != 2 {
		t.Fatalf("invalid parent: %+v %v", result, err)
	}
	second := call(http.MethodPost, "/api/v1/downloads/batch",
		`{"products":["goes18:ABI-L1b-RadF","modis:sample"],"start":"2023-05-03","end":"2023-05-05"}`, key)
	var again struct {
		Created bool                `json:"created"`
		Group   state.DownloadGroup `json:"group"`
	}
	if second.Code != http.StatusAccepted || json.Unmarshal(second.Body.Bytes(), &again) != nil || again.Created || again.Group.ID != result.Group.ID {
		t.Fatalf("duplicate parent not recognized: %d %+v", second.Code, again)
	}
	path := "/api/v1/downloads/batch/" + strconv.FormatInt(result.Group.ID, 10)
	if got := call(http.MethodGet, path, "", otherKey); got.Code != http.StatusNotFound {
		t.Fatalf("other owner read private parent: %d", got.Code)
	}
	if got := call(http.MethodGet, path, "", key); got.Code != http.StatusOK {
		t.Fatalf("owner cannot read batch status: %d", got.Code)
	}
	if got := call(http.MethodGet, "/api/v1/downloads/batch", "", key); got.Code != http.StatusOK {
		t.Fatalf("cannot list parent orders: %d", got.Code)
	}
}
