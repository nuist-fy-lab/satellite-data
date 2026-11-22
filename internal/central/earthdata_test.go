package central

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLAADSFetchReusesSavedEarthdataCredential(t *testing.T) {
	for _, source := range []string{"modis", "viirs"} {
		t.Run(source, func(t *testing.T) {
			store := testStore(t)
			earthdataID, _ := addAccount(t, store, "calipso", "earthdata", Secret{Token: "fixture-earthdata"})
			addAccount(t, store, source, "laads", Secret{Token: "fixture-laads"})
			account, err := store.UpsertProfile(t.Context(), ProfileSpec{AccountSpec: AccountSpec{
				Alias: source + "-earthdata", SourceID: source, CredentialID: &earthdataID,
				ProxyMode: "direct", Enabled: true,
			}})
			if err != nil || account.CredentialID == nil || *account.CredentialID != earthdataID {
				t.Fatalf("bind saved Earthdata credential: %v", err)
			}

			tests := []struct {
				host, authorization string
				status              int
			}{
				{laadsArchiveHost, "Bearer fixture-laads", http.StatusPartialContent},
				{laadsCloudHost, "Bearer fixture-earthdata", http.StatusPartialContent},
				{laadsCloudHost, "Bearer fixture-earthdata", http.StatusForbidden},
			}
			for _, tt := range tests {
				service := NewService(store, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Hostname() != tt.host || r.Header.Get("Authorization") != tt.authorization ||
						r.Header.Get("Range") != "bytes=3-5" || r.Header.Get("If-Range") != "fixture-etag" {
						t.Fatal("fetch did not use the expected endpoint, credential or resume headers")
					}
					response := reply(tt.status, "def")
					response.Header.Set("Content-Range", "bytes 3-5/6")
					return response, nil
				})})
				body, _ := json.Marshal(map[string]string{
					"source_id": source, "url": "https://" + tt.host + "/fixture.nc",
					"range": "bytes=3-5", "if_range": "fixture-etag",
				})
				w := httptest.NewRecorder()
				service.Gateway().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/fetch", bytes.NewReader(body)))
				if w.Code != tt.status || (tt.status == 206 && (w.Body.String() != "def" || w.Header().Get("Content-Range") != "bytes 3-5/6")) {
					t.Fatalf("unexpected response from %s: %d %s", tt.host, w.Code, w.Body.String())
				}
			}
			if _, err := NewService(store).probeRoute(t.Context(), "calipso"); err != nil {
				t.Fatal("LAADS-specific 403 made the shared credential unavailable to CALIPSO:", err)
			}
			account, err = store.UpsertProfile(t.Context(), ProfileSpec{
				AccountSpec:    AccountSpec{ID: account.ID, Alias: account.Alias, SourceID: source, ProxyMode: "direct", Enabled: true},
				CredentialKind: "earthdata", Secret: Secret{Token: "fixture-replacement"},
			})
			if err != nil {
				t.Fatal("replace the profile's Earthdata token:", err)
			}
			route, err := NewService(store, &http.Client{}).resolveFetch(t.Context(), source, nil, nil, laadsCloudHost)
			if err != nil || route.credentialKind != "earthdata" || route.credentialID == earthdataID || route.credentialID != *account.CredentialID {
				t.Fatal("profile did not retain the Earthdata kind with a separate new credential:", err)
			}
		})
	}
}

func TestLAADSEarthdataRedirects(t *testing.T) {
	for _, flow := range []string{"signed download", "interactive login", "wrong callback"} {
		t.Run(flow, func(t *testing.T) {
			store := testStore(t)
			addAccount(t, store, "modis", "earthdata", Secret{Token: "fixture-earthdata"})
			asset := "https://" + laadsCloudHost + "/fixture.nc"
			callback := "https://" + laadsCloudHost + "/login"
			if flow == "wrong callback" {
				callback = "https://other.earthdatacloud.nasa.gov/login"
			}
			login := "https://" + earthdataLoginHost + "/oauth/authorize?redirect_uri=" + url.QueryEscape(callback)
			signed := "https://fixture.s3.us-west-2.amazonaws.com/fixture.nc"
			redirect := func(location string) *http.Response {
				response := reply(302, "")
				response.Header.Set("Location", location)
				return response
			}
			calls := 0
			service := NewService(store, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Hostname() {
				case laadsCloudHost:
					if r.Header.Get("Authorization") != "Bearer fixture-earthdata" {
						t.Fatal("Earthdata bearer missing at the archive")
					}
					if r.URL.Path == "/login" {
						if r.Header.Get("Cookie") != "session=fixture-session" {
							t.Fatal("archive session did not survive Earthdata Login")
						}
						return redirect(signed), nil
					}
					response := redirect(login)
					response.Header.Set("Set-Cookie", "session=fixture-session; Path=/; Secure")
					return response, nil
				case earthdataLoginHost:
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Fatal("archive authorization crossed into Earthdata Login")
					}
					if flow == "interactive login" {
						response := reply(200, "<html>Login</html>")
						response.Header.Set("Content-Type", "text/html; charset=utf-8")
						return response, nil
					}
					return redirect(callback), nil
				default:
					if r.URL.String() != signed || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Fatal("authorization crossed into object storage")
					}
					if r.Header.Get("Range") != "bytes=3-5" || r.Header.Get("If-Range") != "fixture-etag" {
						t.Fatal("resume headers were lost during redirect")
					}
					return reply(206, "def"), nil
				}
			})})
			body := `{"source_id":"modis","url":"` + asset + `","range":"bytes=3-5","if_range":"fixture-etag"}`
			w := httptest.NewRecorder()
			service.Gateway().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/fetch", strings.NewReader(body)))
			wantStatus := map[string]int{"signed download": 206, "interactive login": 401, "wrong callback": 502}
			wantCalls := map[string]int{"signed download": 4, "interactive login": 2, "wrong callback": 1}
			if w.Code != wantStatus[flow] || calls != wantCalls[flow] {
				t.Fatalf("got HTTP %d after %d requests", w.Code, calls)
			}
		})
	}
}
