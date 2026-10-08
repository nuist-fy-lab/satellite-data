package central

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(code int, text string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}
}
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	vault, err := OpenVault(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dir, "access.db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func addAccount(t *testing.T, store *Store, source, kind string, secret Secret) (int64, int64) {
	t.Helper()
	credential, err := store.UpsertCredential(t.Context(), CredentialSpec{Alias: source, Kind: kind, Secret: secret, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.UpsertAccount(t.Context(), AccountSpec{Alias: source, SourceID: source, CredentialID: &credential.ID, ProxyMode: "direct", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return credential.ID, account.ID
}

func TestVaultAuthenticatedEncryptionAndPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	v, err := OpenVault(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := v.Seal([]byte("fixture-private-value"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := v.Seal([]byte("fixture-private-value"))
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("fixture-private-value")) {
		t.Fatal("missing randomized encryption")
	}
	reopened, err := OpenVault(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := reopened.Open(a)
	if err != nil || string(plain) != "fixture-private-value" {
		t.Fatal("restart decryption failed")
	}
	a[len(a)-1] ^= 1
	if _, err := v.Open(a); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(path); err == nil {
		t.Fatal("public key file accepted")
	}
}

func TestMODISReusesConnectionOnlyWithinTheSameCredentialRoute(t *testing.T) {
	store := testStore(t)
	credentialID, _ := addAccount(t, store, "modis", "laads", Secret{Token: "fixture-laads"})
	service := NewService(store)
	first, err := service.resolve(t.Context(), "modis", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.resolve(t.Context(), "modis", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.keepIdleConnections || !second.keepIdleConnections || first.client.Transport == second.client.Transport {
		t.Fatal("concurrent MODIS requests must use separate route-isolated transports")
	}
	defer first.release()
	defer second.release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		wasReused := false
		request, _ := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { wasReused = info.Reused },
		}), http.MethodGet, server.URL, nil)
		response, err := first.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if (i == 1) != wasReused {
			t.Fatalf("attempt %d reused=%t", i, wasReused)
		}
	}
	if _, err := store.UpsertCredential(t.Context(), CredentialSpec{ID: credentialID, Alias: "modis", Kind: "laads", Secret: Secret{Token: "updated-laads"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.resolve(t.Context(), "modis", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer updated.release()
	if first.client.Transport == updated.client.Transport {
		t.Fatal("credential rotation reused another credential's transport")
	}
	for _, pool := range service.modisPools {
		for _, transport := range pool.all {
			transport.CloseIdleConnections()
		}
	}
}

func TestMODISParallelHTTPSGranulesUseIndependentTCPConnections(t *testing.T) {
	// LAADS prefers HTTP/2, but concurrent granules must not share one TCP
	// congestion window (the root cause of the production slowdown).
	entered := make(chan string, 2)
	release := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.RemoteAddr
		<-release
		_, _ = io.WriteString(w, "granule")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	service := NewService(nil)
	first, releaseFirst, err := service.leaseMODISTransport(t.Context(), "modis:isolation-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()
	second, releaseSecond, err := service.leaseMODISTransport(t.Context(), "modis:isolation-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if first == second {
		t.Fatal("two concurrent leases shared the same transport")
	}
	defer first.CloseIdleConnections()
	defer second.CloseIdleConnections()
	first.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	second.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	clients := []*http.Client{{Transport: first}, {Transport: second}}
	type result struct {
		proto int
		err   error
	}
	results := make(chan result, 2)
	for _, client := range clients {
		go func(client *http.Client) {
			response, err := client.Get(server.URL)
			if err != nil {
				results <- result{err: err}
				return
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			response.Body.Close()
			results <- result{proto: response.ProtoMajor, err: readErr}
		}(client)
	}
	var remotes [2]string
	for i := range remotes {
		select {
		case remotes[i] = <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("parallel requests did not reach the server independently")
		}
	}
	close(release)
	if remotes[0] == remotes[1] {
		t.Fatalf("concurrent MODIS transfers shared one TCP socket: %v", remotes)
	}
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || got.proto != 2 {
				t.Fatalf("MODIS transfer must retain LAADS-compatible HTTP/2: %+v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("parallel HTTP/2 transfer did not finish")
		}
	}
	// A subsequent transfer should reuse the idle HTTP/2 connection in its
	// leased transport, rather than always allocating a new TLS socket.
	var reused bool
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(),
		&httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}),
		http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := clients[0].Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil || !reused || response.ProtoMajor != 2 {
		t.Fatalf("sequential HTTP/2 reuse failed: reused=%t proto=%s err=%v", reused, response.Proto, err)
	}
}

func TestMODISTransportLeaseLimitsConcurrentStreams(t *testing.T) {
	service := NewService(nil)
	var transports []*http.Transport
	var releases []func()
	for range 8 {
		transport, release, err := service.leaseMODISTransport(t.Context(), "same-account-route", nil)
		if err != nil {
			t.Fatal(err)
		}
		transports = append(transports, transport)
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
		for _, transport := range transports {
			transport.CloseIdleConnections()
		}
	}()
	for i := 0; i < len(transports); i++ {
		for j := i + 1; j < len(transports); j++ {
			if transports[i] == transports[j] {
				t.Fatal("two simultaneous MODIS leases shared a TCP transport")
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if _, _, err := service.leaseMODISTransport(ctx, "same-account-route", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ninth concurrent lease should wait or cancel: %v", err)
	}
	// Returning a lease must unblock a pending stream, without changing
	// its transport or forcing the TLS keep-alive connection closed.
	releases[0]()
	releases = releases[1:]
	again, releaseAgain, err := service.leaseMODISTransport(t.Context(), "same-account-route", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAgain()
	if again != transports[0] {
		t.Fatal("a returned MODIS transport was not reused")
	}
}

func TestMODISAccessStatusRemainsAvailableWithAllTransportLeasesBusy(t *testing.T) {
	store := testStore(t)
	_, _ = addAccount(t, store, "modis", "laads", Secret{Token: "fixture-laads"})
	service := NewService(store)
	var downloads []route
	for range 8 {
		r, err := service.resolve(t.Context(), "modis", nil)
		if err != nil {
			t.Fatal(err)
		}
		downloads = append(downloads, r)
	}
	defer func() {
		for _, r := range downloads {
			r.release()
		}
	}()
	for _, pool := range service.modisPools {
		if len(pool.idle) != 0 {
			t.Fatal("all eight slots should be occupied")
		}
	}
	handler := service.Gateway()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/access/status", nil).WithContext(ctx))
		cancel()
		if response.Code != http.StatusOK {
			t.Fatalf("access status call %d returned %d", i, response.Code)
		}
		var body struct {
			Providers map[string]bool `json:"providers"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Providers["modis"] {
			t.Fatalf("access status call %d lost MODIS route: %s err=%v", i, response.Body.String(), err)
		}
	}
	if len(service.modisPools) != 1 {
		t.Fatalf("unexpected MODIS pool count: %d", len(service.modisPools))
	}
	for _, pool := range service.modisPools {
		if got := len(pool.idle); got != 0 {
			t.Fatalf("status checks improperly took or returned a busy transport: idle=%d", got)
		}
		for _, transport := range pool.all {
			transport.CloseIdleConnections()
		}
	}
}

func TestAllCredentialKindsAreCentralAndSecretsAreWriteOnly(t *testing.T) {
	s := testStore(t)
	for _, tc := range []struct {
		source, kind string
		secret       Secret
	}{
		{"modis", "laads", Secret{Token: "fixture-laads"}},
		{"calipso", "earthdata", Secret{Token: "fixture-earthdata"}},
		{"earthcare", "earthcare", Secret{Token: "fixture-offline"}},
		{"mtg", "eumetsat", Secret{ConsumerKey: "fixture-key", ConsumerSecret: "fixture-secret"}},
		{"cloudsat", "cloudsat", Secret{Username: "fixture-user", Password: "fixture-password"}},
		{"era5", "cds", Secret{Token: "fixture-cds"}},
	} {
		id, _ := addAccount(t, s, tc.source, tc.kind, tc.secret)
		if _, err := s.UpsertCredential(t.Context(), CredentialSpec{ID: id, Alias: tc.source, Kind: tc.kind, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		plain, err := s.readSecret(t.Context(), "credentials", id)
		if err != nil {
			t.Fatal(err)
		}
		var restored Secret
		if json.Unmarshal(plain, &restored) != nil || restored != tc.secret {
			t.Fatal("blank edit did not preserve credential")
		}
	}
	proxy, err := s.UpsertProxy(t.Context(), ProxySpec{Alias: "shared", URL: "socks5://fixture-proxy-user:fixture-proxy-password@proxy.example:1080", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertAccount(t.Context(), AccountSpec{Alias: "anonymous", SourceID: "goes", ProxyMode: "fixed", ProxyID: &proxy.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pools, err := s.Pools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(pools)
	for _, value := range []string{"fixture-", "encrypted_secret", "socks5://"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("secret in metadata")
		}
	}
	if len(pools.Credentials) != 6 || len(pools.Accounts) != 7 || pools.Proxies[0].Storage != "central" {
		t.Fatal("pool ownership or count mismatch")
	}
	if _, err := s.UpsertAccount(t.Context(), AccountSpec{Alias: "wrong", SourceID: "era5", CredentialID: &pools.Credentials[0].ID, ProxyMode: "direct", Enabled: true}); err == nil {
		t.Fatal("wrong credential kind accepted")
	}
	if err := s.Delete(t.Context(), "proxies", proxy.ID); err != nil {
		t.Fatal(err)
	}
	pools, err = s.Pools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pools.Accounts {
		if a.SourceID == "goes" && a.ProxyID != nil {
			t.Fatal("deleted proxy binding survived")
		}
	}
}

func TestExpiryMovedToCentre(t *testing.T) {
	s := testStore(t)
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	claims, _ := json.Marshal(map[string]any{"exp": expires.Unix()})
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	for _, kind := range []string{"laads", "earthdata", "earthcare"} {
		plain, _ := json.Marshal(Secret{Token: token})
		payload, _ := s.vault.Seal(plain)
		expiry, err := InspectCredentialExpiry(kind, payload, s.vault)
		if err != nil || expiry.Status != expiryKnown || expiry.ExpiresAt != expires.Format(time.RFC3339) {
			t.Fatal("JWT expiry was lost")
		}
	}
	plain, _ := json.Marshal(Secret{Token: "opaque-fixture"})
	payload, _ := s.vault.Seal(plain)
	expiry, err := InspectCredentialExpiry("cds", payload, s.vault)
	if err != nil || expiry.Status != expiryNotProvided || expiry.ExpiresAt != "" {
		t.Fatal("fabricated CDS expiry")
	}
}

func TestGatewayRangesAndHostBoundaries(t *testing.T) {
	s := testStore(t)
	addAccount(t, s, "modis", "laads", Secret{Token: "fixture-bearer"})
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-bearer" || r.Header.Get("Range") != "bytes=3-" || r.Header.Get("If-Range") != "fixture-etag" {
			t.Fatal("missing central authorization or resume headers")
		}
		resp := reply(206, "def")
		resp.Header.Set("Content-Length", "3")
		resp.Header.Set("Content-Range", "bytes 3-5/6")
		resp.Header.Set("ETag", "fixture-etag")
		return resp, nil
	})}
	service := NewService(s, client)
	fetch := func(locator string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"source_id": "modis", "url": locator, "range": "bytes=3-", "if_range": "fixture-etag"})
		w := httptest.NewRecorder()
		service.Gateway().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/fetch", bytes.NewReader(body)))
		return w
	}
	w := fetch("https://ladsweb.modaps.eosdis.nasa.gov/data/fixture.h5")
	if w.Code != 206 || w.Body.String() != "def" || w.Header().Get("Content-Range") != "bytes 3-5/6" || w.Header().Get("ETag") != "fixture-etag" {
		t.Fatal("resume response was not preserved")
	}
	for _, locator := range []string{"https://127.0.0.1/data", "http://ladsweb.modaps.eosdis.nasa.gov/data", "https://ladsweb.modaps.eosdis.nasa.gov:444/data", "https://user:pass@ladsweb.modaps.eosdis.nasa.gov/data", "https://ladsweb.modaps.eosdis.nasa.gov.evil.example/data"} {
		if fetch(locator).Code != 400 {
			t.Fatal("unsafe fetch URL accepted")
		}
	}
	if calls != 1 {
		t.Fatal("unsafe URL reached network")
	}
	pools, _ := s.Pools(t.Context())
	if pools.Accounts[0].LastUsedAt == "" || pools.Accounts[0].LastSuccessAt == "" {
		t.Fatal("centre did not record transfer outcome")
	}
}

func TestGatewayRedirectStripsSecretsAndRejectsForeignHosts(t *testing.T) {
	s := testStore(t)
	addAccount(t, s, "modis", "laads", Secret{Token: "fixture-bearer"})
	for _, target := range []string{"https://bucket.s3.amazonaws.com/fixture.h5", "https://evil.example/fixture.h5"} {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				resp := reply(302, "")
				resp.Header.Set("Location", target)
				return resp, nil
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("credential leaked across redirect")
			}
			return reply(200, "safe"), nil
		})}
		service := NewService(s, client)
		w := httptest.NewRecorder()
		service.Gateway().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/fetch", strings.NewReader(`{"source_id":"modis","url":"https://ladsweb.modaps.eosdis.nasa.gov/fixture.h5"}`)))
		if strings.Contains(target, "evil.example") {
			if calls != 1 || w.Code != 502 {
				t.Fatal("foreign redirect was followed")
			}
		} else if w.Code != 200 || calls != 2 {
			t.Fatal("safe redirect failed")
		}
	}
}

func TestCDSJobPinsCredentialAcrossRestartAndRejectsMutation(t *testing.T) {
	s := testStore(t)
	id, _ := addAccount(t, s, "era5", "cds", Secret{Token: "fixture-cds"})
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(r.URL.Path, "processes") && r.Method == http.MethodGet {
			return reply(200, `{"id":"reanalysis-era5-single-levels","inputs":{"variable":{}}}`), nil
		}
		if r.Header.Get("PRIVATE-TOKEN") != "fixture-cds" {
			t.Fatal("wrong CDS identity")
		}
		if r.Method == http.MethodPost {
			return reply(202, `{"jobID":"fixture-job","status":"accepted"}`), nil
		}
		return reply(200, `{"jobID":"fixture-job","status":"successful"}`), nil
	})}
	service := NewService(s, client)
	job, err := service.Submit(t.Context(), era5.Submission{CollectionID: "era5:reanalysis-era5-single-levels", Inputs: map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)}})
	if err != nil || job.ID != "fixture-job" {
		t.Fatal("CDS submission failed", err)
	}
	restarted := NewService(s, client)
	if _, err := restarted.Job(t.Context(), job.ID); err != nil {
		t.Fatal("restart lost job binding", err)
	}
	if _, err := s.UpsertCredential(t.Context(), CredentialSpec{ID: id, Alias: "era5", Kind: "cds", Secret: Secret{Token: "changed-fixture"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := calls
	if _, err := restarted.Job(t.Context(), job.ID); !errors.Is(err, ErrChangedCredential) {
		t.Fatal("changed credential accepted", err)
	}
	if calls != before {
		t.Fatal("changed identity reached CDS")
	}
	if _, err := restarted.Job(t.Context(), "unowned-job"); err == nil {
		t.Fatal("unowned job queried")
	}
}

func TestEarthCAREOfflineTokenExchangeMovedToCentre(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != earthcareTokenEndpoint {
			t.Fatal("wrong token endpoint")
		}
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil || values.Get("grant_type") != "refresh_token" || values.Get("refresh_token") != "fixture-offline" || values.Get("client_id") != earthcareClientID {
			t.Fatal("wrong refresh form")
		}
		return reply(200, `{"access_token":"fixture-short-access","expires_in":300}`), nil
	})}
	a := &earthcareToken{client: client, offline: "fixture-offline"}
	r, _ := http.NewRequest(http.MethodGet, "https://catalog.maap.eo.esa.int/fixture.h5", nil)
	if err := a.Authorize(context.Background(), r); err != nil || r.Header.Get("Authorization") != "Bearer fixture-short-access" {
		t.Fatal("central OAuth exchange failed", err)
	}
}
