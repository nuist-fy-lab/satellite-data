package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
)

// Keys control operational APIs, never the web-only credential or key manager.
func (s *Server) withPortalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/login" || r.URL.Path == "/login.js" || r.URL.Path == "/styles.css" || r.URL.Path == "/app.js" || r.URL.Path == "/favicon.ico") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && (r.URL.Path == "/login" || r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/access/login") && r.Header.Get("Authorization") == "" {
			if !sameOrigin(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		isAPI := strings.HasPrefix(r.URL.Path, "/api/")
		if r.Header.Get("Authorization") != "" {
			w.Header().Set("Cache-Control", "no-store")
			if _, err := s.cliUser(r); err != nil {
				writeError(w, http.StatusUnauthorized, errors.New("invalid or revoked CLI API key"))
				return
			}
			if !cliRouteAllowed(r.Method, r.URL.Path) {
				writeError(w, http.StatusForbidden, errors.New("web session required for access and API key management"))
				return
			}
		} else if s.portalAuth {
			w.Header().Set("Cache-Control", "no-store")
			if _, err := s.webUser(r); err != nil {
				if isAPI {
					writeError(w, http.StatusUnauthorized, errors.New("portal session or CLI API key required"))
				} else {
					http.Redirect(w, r, "/login", http.StatusSeeOther)
				}
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(w, r) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func cliRouteAllowed(method, path string) bool {
	if method == http.MethodPost {
		return path == "/api/v1/downloads" || path == "/api/index" || path == "/api/v1/era5/requests"
	}
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/api/v1/sources", "/api/v1/catalog", "/api/v1/search", "/api/v1/downloads", "/api/v1/downloads/updates", "/api/v1/downloader/providers", "/api/v1/downloader/providers/stream", "/api/v1/index/stream", "/api/v1/monitor/downloads", "/api/v1/monitor/links", "/api/v1/era5/schema", "/api/products", "/api/year", "/api/day":
		return true
	}
	return strings.HasPrefix(path, "/api/v1/downloads/") || strings.HasPrefix(path, "/api/coverage/") || era5JobRoute(path)
}

func era5JobRoute(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/era5/requests/")
	if !ok {
		return false
	}
	id := strings.TrimSuffix(rest, "/result")
	return id != "" && !strings.Contains(id, "/")
}

func (s *Server) cliUser(r *http.Request) (authstore.User, error) {
	if s.auth == nil {
		return authstore.User{}, errors.New("authentication unavailable")
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return authstore.User{}, errors.New("API key required")
	}
	return s.auth.AuthenticateAPIKey(r.Context(), strings.TrimSpace(token))
}

func (s *Server) orderUser(w http.ResponseWriter, r *http.Request) (authstore.User, bool) {
	if !s.portalAuth {
		return authstore.User{Role: "admin"}, true
	}
	var user authstore.User
	var err error
	if r.Header.Get("Authorization") != "" {
		if !sameOrigin(w, r) {
			return authstore.User{}, false
		}
		user, err = s.cliUser(r)
	} else {
		user, err = s.webUser(r)
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("portal session or CLI API key required"))
		return authstore.User{}, false
	}
	return user, true
}

func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeError(w, http.StatusForbidden, errors.New("cross-origin portal mutation rejected"))
			return false
		}
	}
	return true
}

func (s *Server) portalSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, err := s.webUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("portal web session required"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user, err := s.webUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("portal web session required"))
		return
	}
	keys, err := s.auth.ListAPIKeys(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("API key store unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) apiKeyMutation(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.webUser(r); err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("portal web session required"))
		return false
	}
	if !sameOrigin(w, r) {
		return false
	}
	if r.Header.Get("X-Satellite-Action") != "1" {
		writeError(w, http.StatusForbidden, errors.New("missing action header"))
		return false
	}
	return true
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeyMutation(w, r) {
		return
	}
	user, _ := s.webUser(r)
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeRequestJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if name := strings.TrimSpace(body.Name); name == "" || len(name) > 80 {
		writeError(w, http.StatusBadRequest, errors.New("API key name must be 1 to 80 characters"))
		return
	}
	key, secret, err := s.auth.CreateAPIKey(r.Context(), user.ID, body.Name)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("API key creation failed"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "api_key": secret})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeyMutation(w, r) {
		return
	}
	user, _ := s.webUser(r)
	if err := s.auth.RevokeAPIKey(r.Context(), user.ID, r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, errors.New("API key not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
