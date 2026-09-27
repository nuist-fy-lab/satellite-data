package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestAccessManagementRequiresAdminSession(t *testing.T) {
	dir := t.TempDir()
	dataStore, err := state.Open(filepath.Join(dir, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	auth, err := authstore.Open(filepath.Join(dir, "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	if _, err := auth.CreateUser(t.Context(), "admin", "correct horse battery staple", "admin"); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, dataStore, nil, auth, time.Hour)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/access/pools", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized access status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	login := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/login", strings.NewReader("{\"username\":\"admin\",\"password\":\"correct horse battery staple\"}"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, req)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if candidate.Name == accessSessionCookie {
			cookie = candidate
			break
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("HttpOnly access session cookie not returned")
	}

	session := httptest.NewRecorder()
	sessionReq := httptest.NewRequest(http.MethodGet, "/api/v1/access/session", nil)
	sessionReq.AddCookie(cookie)
	handler.ServeHTTP(session, sessionReq)
	if session.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", session.Code, session.Body.String())
	}

	pools := httptest.NewRecorder()
	poolsReq := httptest.NewRequest(http.MethodGet, "/api/v1/access/pools", nil)
	poolsReq.AddCookie(cookie)
	handler.ServeHTTP(pools, poolsReq)
	if pools.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated pools status=%d body=%s", pools.Code, pools.Body.String())
	}

	vault, err := central.OpenVault(filepath.Join(dir, "central.key"))
	if err != nil {
		t.Fatal(err)
	}
	accessStore, err := central.OpenStore(filepath.Join(dir, "central.sqlite3"), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer accessStore.Close()
	handler = NewWithAuth(registry, dataStore, nil, auth, time.Hour, central.NewService(accessStore))
	requestBody := `{"alias":"ERA5","source_id":"era5","proxy_mode":"direct","enabled":true,"secret":{"token":"fixture-CDS"}}`
	profile := func(origin string, withCookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/access/profiles", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if withCookie {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := profile("", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed profile status=%d", w.Code)
	}
	if w := profile("https://other.example", true); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin profile status=%d", w.Code)
	}
	if w := profile("http://example.com", true); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fixture-CDS") {
		t.Fatalf("profile save status=%d or secret leaked", w.Code)
	}
}
