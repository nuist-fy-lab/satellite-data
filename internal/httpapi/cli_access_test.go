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

	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
)

func TestCLIAccessManagesSavedProfileReferences(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	auth, err := authstore.Open(filepath.Join(dir, "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	keyFor := func(role string) (string, authstore.User, string) {
		t.Helper()
		user, err := auth.CreateUser(ctx, role+"-access", "fixture-long-password", role)
		if err != nil {
			t.Fatal(err)
		}
		item, key, err := auth.CreateAPIKey(ctx, user.ID, role+" access")
		if err != nil {
			t.Fatal(err)
		}
		return key, user, item.ID
	}
	adminKey, admin, adminKeyID := keyFor("admin")
	userKey, _, _ := keyFor("user")
	session, err := auth.CreateSession(ctx, admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := central.OpenVault(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := central.OpenStore(filepath.Join(dir, "access.sqlite3"), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credential := func(kind string) int64 {
		t.Helper()
		item, err := store.UpsertCredential(ctx, central.CredentialSpec{
			Alias: kind, Kind: kind, Enabled: true, Secret: central.Secret{Token: "fixture-" + kind + "-secret"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	edlID, cdsID := credential("earthdata"), credential("cds")
	proxy, err := store.UpsertProxy(ctx, central.ProxySpec{
		Alias: "fixed proxy", URL: "socks5://fixture-user:fixture-password@proxy.example:1080",
		Providers: []string{"modis"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(nil, nil, nil, auth, time.Hour, central.NewService(store))
	request := func(method, suffix, key, origin string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, cliAccessPath+suffix, strings.NewReader(string(body)))
		r.AddCookie(&http.Cookie{Name: accessSessionCookie, Value: session})
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, tc := range []struct {
			key    string
			status int
		}{{"", 401}, {"invalid", 401}, {userKey, 403}} {
			if w := request(method, "", tc.key, "", nil); w.Code != tc.status {
				t.Fatalf("%s unauthorized response %d, want %d", method, w.Code, tc.status)
			}
		}
	}
	if w := request(http.MethodPost, "", adminKey, "https://foreign.example", nil); w.Code != 403 {
		t.Fatalf("cross-origin update status=%d", w.Code)
	}
	if w := request(http.MethodPost, "", adminKey, "", map[string]any{"secret": "fixture"}); w.Code != 400 {
		t.Fatalf("raw credential body accepted status=%d", w.Code)
	}
	profile := func(spec central.AccountSpec, wantStatus int) cliProfileView {
		t.Helper()
		w := request(http.MethodPost, "", adminKey, "", spec)
		if w.Code != wantStatus {
			t.Fatalf("save %s returned %d, want %d: %s", spec.SourceID, w.Code, wantStatus, w.Body.String())
		}
		var saved cliProfileView
		if wantStatus == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.ID == 0 {
				t.Fatalf("save returned invalid profile: %v", err)
			}
		}
		return saved
	}
	profile(central.AccountSpec{Alias: "wrong credential", SourceID: "modis", CredentialID: &cdsID, ProxyMode: "direct"}, 400)
	modis := profile(central.AccountSpec{Alias: "MODIS", SourceID: "modis", CredentialID: &edlID, ProxyMode: "fixed", ProxyID: &proxy.ID, Enabled: true}, 200)
	profile(central.AccountSpec{Alias: "ERA5", SourceID: "era5", CredentialID: &cdsID, ProxyMode: "direct", Enabled: true}, 200)
	profile(central.AccountSpec{Alias: "GOES", SourceID: "goes", ProxyMode: "direct", Enabled: true}, 200)
	updated := profile(central.AccountSpec{ID: modis.ID, Alias: modis.Alias, SourceID: "modis", ProxyMode: "direct"}, 200)
	if updated.CredentialID == nil || *updated.CredentialID != edlID || updated.ProxyID != nil || updated.Enabled {
		t.Fatalf("update did not preserve the saved credential and apply routing settings: %+v", updated)
	}
	w := request(http.MethodGet, "", adminKey, "", nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list status=%d cache=%s", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, value := range []string{"fixture-", "proxy.example", "secret_hint", "endpoint_hint"} {
		if strings.Contains(w.Body.String(), value) {
			t.Fatalf("CLI access list exposed %s", value)
		}
	}
	var listed struct {
		Credentials []cliCredentialView `json:"credentials"`
		Proxies     []cliProxyView      `json:"proxies"`
		Profiles    []cliProfileView    `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Credentials) != 2 || len(listed.Proxies) != 1 || len(listed.Profiles) != 3 {
		t.Fatalf("unexpected CLI references: %+v, %v", listed, err)
	}
	if w := request(http.MethodDelete, "?id="+strconv.FormatInt(modis.ID, 10), adminKey, "", nil); w.Code != 204 {
		t.Fatalf("delete status=%d", w.Code)
	}
	pools, err := store.Pools(ctx)
	if err != nil || len(pools.Accounts) != 2 || len(pools.Credentials) != 2 || len(pools.Proxies) != 1 {
		t.Fatalf("delete did not preserve credential and proxy: %+v, %v", pools, err)
	}
	if err := auth.RevokeAPIKey(ctx, admin.ID, adminKeyID); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "", adminKey, "", nil); w.Code != 401 {
		t.Fatalf("revoked administrator key status=%d", w.Code)
	}
}
