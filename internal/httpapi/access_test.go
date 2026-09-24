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
}
