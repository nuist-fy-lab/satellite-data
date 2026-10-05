package central

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGatewayCloudSatSessionRedirects(t *testing.T) {
	const origin = "https://dpc-thredds.cira.colostate.edu"
	const filePath = "/thredds/fileServer/cloudsat-data/fixture.hdf"
	const authPath = "/thredds/restrictedAccess/DPCData"
	for _, tc := range []struct {
		name, requestedRange, ifRange, contentRange, body string
		status                                            int
	}{
		{name: "whole file", body: "0123456789", status: http.StatusOK},
		{name: "resume", requestedRange: "bytes=3-5", ifRange: "fixture-etag", contentRange: "bytes 3-5/10", body: "345", status: http.StatusPartialContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			addAccount(t, store, "cloudsat", "cloudsat", Secret{Username: "fixture-user@example.com", Password: "fixture-password"})
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				username, password, ok := r.BasicAuth()
				if !ok || username != "fixture-user@example.com" || password != "fixture-password" || r.URL.User != nil {
					t.Fatal("Basic authentication must preserve the email without URL credentials")
				}
				if r.Header.Get("Range") != tc.requestedRange || r.Header.Get("If-Range") != tc.ifRange {
					t.Fatal("resume headers were lost during authentication redirects")
				}
				cookie, _ := r.Cookie("JSESSIONID")
				redirect := func(location, session string) *http.Response {
					resp := reply(http.StatusTemporaryRedirect, "")
					resp.Header.Set("Location", origin+location)
					resp.Header.Set("Set-Cookie", (&http.Cookie{Name: "JSESSIONID", Value: session, Path: "/thredds", Secure: true, HttpOnly: true}).String())
					return resp
				}
				switch calls {
				case 1:
					if r.URL.Path != filePath || cookie != nil {
						t.Fatal("file request must start without a previous session")
					}
					return redirect(authPath, "fixture-start"), nil
				case 2:
					if r.URL.Path != authPath || cookie == nil || cookie.Value != "fixture-start" {
						return reply(http.StatusForbidden, "missing THREDDS session"), nil
					}
					return redirect(filePath, "fixture-authenticated"), nil
				case 3:
					if r.URL.Path != filePath || cookie == nil || cookie.Value != "fixture-authenticated" {
						return reply(http.StatusForbidden, "missing authenticated session"), nil
					}
					resp := reply(tc.status, tc.body)
					resp.Header.Set("Content-Type", "application/x-hdf")
					resp.Header.Set("ETag", "fixture-etag")
					if tc.contentRange != "" {
						resp.Header.Set("Content-Range", tc.contentRange)
					}
					return resp, nil
				default:
					t.Fatal("unexpected extra authentication request")
					return nil, nil
				}
			})}
			service := NewService(store, client)
			payload, _ := json.Marshal(map[string]string{"source_id": "cloudsat", "url": origin + filePath, "range": tc.requestedRange, "if_range": tc.ifRange})
			w := httptest.NewRecorder()
			service.Gateway().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/fetch", bytes.NewReader(payload)))
			if w.Code != tc.status || w.Body.String() != tc.body || calls != 3 || w.Header().Get("Content-Range") != tc.contentRange {
				t.Fatalf("THREDDS session transfer failed: HTTP %d after %d requests", w.Code, calls)
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Fatal("provider session cookie leaked to the downloader")
			}
			pools, err := store.Pools(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if pools.Accounts[0].State != "ready" || pools.Credentials[0].State != "ready" || pools.Accounts[0].LastSuccessAt == "" {
				t.Fatal("successful session transfer was not recorded as ready")
			}
		})
	}
}

func TestCloudSatSessionCookiesAreFetchLocal(t *testing.T) {
	store := testStore(t)
	addAccount(t, store, "cloudsat", "cloudsat", Secret{Username: "fixture-user@example.com", Password: "fixture-password"})
	location, err := url.Parse("https://dpc-thredds.cira.colostate.edu/thredds/fileServer/fixture.hdf")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	shared.SetCookies(location, []*http.Cookie{{Name: "JSESSIONID", Value: "fixture-shared", Path: "/thredds", Secure: true}})
	client := &http.Client{Jar: shared}
	service := NewService(store, client)
	first, err := service.resolve(t.Context(), "cloudsat", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.client.CloseIdleConnections()
	if first.client.Jar == nil || len(first.client.Jar.Cookies(location)) != 0 {
		t.Fatal("CloudSat inherited another client's session cookies")
	}
	first.client.Jar.SetCookies(location, []*http.Cookie{{Name: "JSESSIONID", Value: "fixture-first", Path: "/thredds", Secure: true}})
	second, err := service.resolve(t.Context(), "cloudsat", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.client.CloseIdleConnections()
	if second.client.Jar == nil || second.client.Jar == first.client.Jar || len(second.client.Jar.Cookies(location)) != 0 {
		t.Fatal("independent CloudSat fetches shared authentication sessions")
	}
	if cookies := shared.Cookies(location); len(cookies) != 1 || cookies[0].Value != "fixture-shared" {
		t.Fatal("CloudSat modified the supplied client's cookie jar")
	}
}

func TestOtherSourcesDoNotEnableCloudSatSessionCookies(t *testing.T) {
	store := testStore(t)
	addAccount(t, store, "modis", "laads", Secret{Token: "fixture-token"})
	service := NewService(store)
	route, err := service.resolve(t.Context(), "modis", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer route.client.CloseIdleConnections()
	if route.client.Jar != nil {
		t.Fatal("CloudSat session behavior enabled for another provider")
	}
}
