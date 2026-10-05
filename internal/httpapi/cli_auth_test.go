package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestCLIAPIKeyWebLifecycleAndAccessIsolation(t *testing.T) {
	dir := t.TempDir()
	auth, err := authstore.Open(filepath.Join(dir, "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	user, err := auth.CreateUser(t.Context(), "operator", "fixture-test-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.CreateSession(t.Context(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(dir, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, store, nil, auth, time.Hour)
	request := func(method, path, body, key, origin string, cookie, action bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie {
			r.AddCookie(&http.Cookie{Name: accessSessionCookie, Value: session})
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if action {
			r.Header.Set("X-Satellite-Action", "1")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/me/api-keys"
	for _, tc := range []struct {
		cookie bool
		action bool
		origin string
		status int
	}{
		{false, true, "", http.StatusUnauthorized},
		{true, false, "", http.StatusForbidden},
		{true, true, "https://other.example", http.StatusForbidden},
	} {
		if w := request(http.MethodPost, path, `{"name":"CLI"}`, "", tc.origin, tc.cookie, tc.action); w.Code != tc.status {
			t.Fatalf("key creation status=%d want=%d", w.Code, tc.status)
		}
	}
	w := request(http.MethodPost, path, `{"name":"CLI"}`, "", "", true, true)
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d", w.Code)
	}
	var created struct {
		Key    authstore.APIKey `json:"key"`
		Secret string           `json:"api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "satellite_v1_") {
		t.Fatal("wrong API key namespace")
	}
	w = request(http.MethodGet, path, "", "", "", true, false)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), "secret_hash") {
		t.Fatal("API key list failed or exposed a secret")
	}
	for _, endpoint := range []string{"/api/v1/access/pools", "/api/v1/access/profiles", "/api/v1/access/central/credentials", "/api/v1/access/accounts", path} {
		for _, withCookie := range []bool{false, true} {
			if w := request(http.MethodGet, endpoint, "", created.Secret, "", withCookie, true); w.Code != http.StatusForbidden {
				t.Fatalf("CLI key accessed %s status=%d", endpoint, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		key    string
		status int
	}{{created.Secret, http.StatusOK}, {"invalid", http.StatusUnauthorized}} {
		if w := request(http.MethodGet, "/api/v1/sources", "", tc.key, "", false, false); w.Code != tc.status {
			t.Fatalf("source query status=%d want=%d", w.Code, tc.status)
		}
	}
	if w := request(http.MethodPost, "/api/v1/downloads", `{}`, "", "", false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous task submission status=%d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/downloads", `{}`, created.Secret, "", false, false); w.Code != http.StatusBadRequest {
		t.Fatalf("key did not reach task validation status=%d", w.Code)
	}
	if w := request(http.MethodDelete, path+"/"+created.Key.ID, "", "", "", true, true); w.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/sources", "", created.Secret, "", false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key accepted status=%d", w.Code)
	}
	_, disabledKey, err := auth.CreateAPIKey(t.Context(), user.ID, "Disabled owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetDisabled(t.Context(), user.Username, true); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/sources", "", disabledKey, "", false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled owner's key accepted status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/sources", "", "", "", false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous source query accepted status=%d", w.Code)
	}
}

func TestPortalLoginCoversEntireWebsiteAndCredentialsNeedNoSecondLogin(t *testing.T) {
	dir := t.TempDir()
	auth, err := authstore.Open(filepath.Join(dir, "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	user, err := auth.CreateUser(t.Context(), "portal-admin", "fixture-long-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := auth.CreateUser(t.Context(), "portal-user", "fixture-long-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, nil, nil, auth, time.Hour)
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Satellite-Action", "1")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/index.html", "/downloads", "/accounts"} {
		if w := request(http.MethodGet, path, "", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
			t.Fatalf("anonymous page %s status=%d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/sources", "/api/products", "/api/year", "/api/v1/monitor/links", "/api/v1/me/api-keys"} {
		if w := request(http.MethodGet, path, "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous API %s status=%d", path, w.Code)
		}
	}
	for _, path := range []string{"/login", "/login.js", "/styles.css", "/healthz"} {
		if w := request(http.MethodGet, path, "", nil); w.Code != http.StatusOK {
			t.Fatalf("login asset %s unavailable status=%d", path, w.Code)
		}
	}
	login := request(http.MethodPost, "/api/v1/auth/login", `{"username":"portal-admin","password":"fixture-long-password"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("portal login status=%d", login.Code)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe portal cookie")
	}
	cookie := cookies[0]
	index := request(http.MethodGet, "/", "", cookie)
	if index.Code != http.StatusOK || strings.Contains(index.Body.String(), "access-login-form") || !strings.Contains(index.Body.String(), "portal-logout") {
		t.Fatal("portal HTML missing or credentials still request a second password")
	}
	for _, path := range []string{"/calendar", "/sources", "/downloads", "/sync", "/monitor", "/cli", "/access"} {
		page := request(http.MethodGet, path, "", cookie)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "id=\"calendar-view\"") {
			t.Fatalf("portal module path %s status=%d", path, page.Code)
		}
	}
	// The same portal cookie reaches the credential handler, not a second login.
	if w := request(http.MethodGet, "/api/v1/access/pools", "", cookie); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("portal admin denied credential manager status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/auth/session", "", cookie); w.Code != http.StatusOK {
		t.Fatalf("portal session status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/login", "", cookie); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatal("signed-in user was asked to log in again")
	}
	readerSession, err := auth.CreateSession(t.Context(), reader.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	readerCookie := &http.Cookie{Name: accessSessionCookie, Value: readerSession}
	if w := request(http.MethodGet, "/api/v1/sources", "", readerCookie); w.Code != http.StatusOK {
		t.Fatalf("portal user query status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/access/pools", "", readerCookie); w.Code != http.StatusForbidden {
		t.Fatal("nonadmin gained credential access")
	}
	created := request(http.MethodPost, "/api/v1/me/api-keys", `{"name":"User CLI"}`, readerCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("portal user could not create own CLI Key status=%d", created.Code)
	}
	var payload struct {
		Secret string `json:"api_key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil)
	r.Header.Set("Authorization", "Bearer "+payload.Secret)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("portal user's CLI Key rejected")
	}
	expired, err := auth.CreateSession(t.Context(), user.ID, -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/", "", &http.Cookie{Name: accessSessionCookie, Value: expired}); w.Code != http.StatusSeeOther {
		t.Fatal("expired portal session accepted")
	}
	if w := request(http.MethodPost, "/api/v1/auth/logout", "", cookie); w.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/sources", "", cookie); w.Code != http.StatusUnauthorized {
		t.Fatal("logged-out cookie still authenticated")
	}
	missingAuth := NewWithAuth(registry, nil, nil, nil, time.Hour)
	w = httptest.NewRecorder()
	missingAuth.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("missing auth store failed open")
	}
}
