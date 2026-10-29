package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessMovedButHealthAPIRemains(t *testing.T) {
	handler := New(nil, nil)
	for _, endpoint := range []struct{ method, path string }{{"GET", "/v1/access"}, {"POST", "/v1/credentials"}, {"DELETE", "/v1/accounts/1"}, {"POST", "/v1/proxies"}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		if w.Code != http.StatusGone {
			t.Fatal("legacy access endpoint still accepts secrets")
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("health API removed")
	}
}
