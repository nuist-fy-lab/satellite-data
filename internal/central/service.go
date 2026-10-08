package central

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

var ErrNoRoute = errors.New("no usable central account; configure this provider in Access Credentials")
var ErrChangedCredential = errors.New("CDS job credential changed; restore the original credential before querying this job")

type Service struct {
	Store         *Store
	clientFactory func(*url.URL) *http.Client
	mu            sync.Mutex
	authorizers   map[string]Authorizer
	// Each pool belongs to one MODIS account, credential revision and network
	// route. Never share authentication cookies or connections across routes.
	modisTransports map[string]*http.Transport
}

func NewService(store *Store, clients ...*http.Client) *Service {
	s := &Service{Store: store, authorizers: make(map[string]Authorizer), modisTransports: make(map[string]*http.Transport)}
	if len(clients) > 0 && clients[0] != nil {
		s.clientFactory = func(*url.URL) *http.Client { copy := *clients[0]; return &copy }
	}
	return s
}

type route struct {
	accountID, credentialID, proxyID                             int64
	accountVersion, credentialVersion, proxyVersion, fingerprint string
	client                                                       *http.Client
	keepIdleConnections                                          bool
	provider                                                     *era5.Provider
	authorizer                                                   Authorizer
}

func (s *Service) resolve(ctx context.Context, source string, binding *Binding) (route, error) {
	kind, err := ProviderKind(source)
	if err != nil {
		return route{}, err
	}
	pools, err := s.Store.Pools(ctx)
	if err != nil {
		return route{}, err
	}
	var hasPool bool
	sort.SliceStable(pools.Proxies, func(i, j int) bool {
		a, b := pools.Proxies[i], pools.Proxies[j]
		if (a.State == "ready") != (b.State == "ready") {
			return a.State == "ready"
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.LastUsedAt < b.LastUsedAt
	})
	for _, account := range pools.Accounts {
		if account.SourceID == source {
			hasPool = true
			break
		}
	}
	if !hasPool && kind == "" {
		pools.Accounts = append(pools.Accounts, downloader.Account{SourceID: source, Enabled: true, ProxyMode: "direct"})
	}
	for _, account := range pools.Accounts {
		if account.SourceID != source || !account.Enabled || account.State == "auth_required" || (kind != "" && account.CredentialID == nil) {
			continue
		}
		if binding != nil && (account.ID != binding.AccountID || account.CredentialID == nil || *account.CredentialID != binding.CredentialID) {
			continue
		}
		credentialReady := kind == ""
		var credentialVersion string
		for _, credential := range pools.Credentials {
			if account.CredentialID != nil && credential.ID == *account.CredentialID && credential.Kind == kind && credential.Enabled && credential.State != "auth_required" {
				credentialReady = true
				credentialVersion = credential.UpdatedAt
				break
			}
		}
		if !credentialReady {
			continue
		}
		var plain []byte
		var secret Secret
		var credentialID int64
		if account.CredentialID != nil {
			credentialID = *account.CredentialID
			plain, err = s.Store.readSecret(ctx, "credentials", credentialID)
			if err != nil || json.Unmarshal(plain, &secret) != nil {
				return route{}, errors.New("central credential could not be opened")
			}
		}
		sum := sha256.Sum256(plain)
		fingerprint := hex.EncodeToString(sum[:])
		if binding != nil && subtle.ConstantTimeCompare([]byte(binding.TokenHash), []byte(fingerprint)) != 1 {
			return route{}, ErrChangedCredential
		}
		var proxyURL *url.URL
		var proxyID int64
		var proxyVersion string
		switch account.ProxyMode {
		case "direct":
		case "fixed":
			if account.ProxyID != nil {
				for _, proxy := range pools.Proxies {
					if proxy.ID == *account.ProxyID && proxy.Enabled && proxyAllows(proxy.Providers, source) {
						proxyID = proxy.ID
						proxyVersion = proxy.UpdatedAt
						break
					}
				}
			}
			if proxyID == 0 {
				continue
			}
		case "auto":
			for _, proxy := range pools.Proxies {
				if proxy.Enabled && proxyAllows(proxy.Providers, source) {
					proxyID = proxy.ID
					proxyVersion = proxy.UpdatedAt
					break
				}
			}
			if proxyID == 0 {
				continue
			}
		default:
			continue
		}
		if proxyID != 0 {
			plain, err := s.Store.readSecret(ctx, "proxies", proxyID)
			if err != nil {
				return route{}, errors.New("central proxy could not be opened")
			}
			u, err := url.Parse(string(plain))
			if err != nil {
				return route{}, errors.New("invalid central proxy")
			}
			proxyURL = u
		}
		var client *http.Client
		var keepIdleConnections bool
		if s.clientFactory != nil {
			client = s.clientFactory(proxyURL)
		} else if source == "modis" {
			// LAADS serves many short granules through the same HTTPS endpoint.
			// Reuse TLS/TCP connections without sharing a cookie jar or credentials
			// between different user accounts, secret revisions or proxy routes.
			key := fmt.Sprintf("%s:%d:%s:%d:%s:%d:%s", source, account.ID, account.UpdatedAt,
				credentialID, fingerprint, proxyID, proxyVersion)
			client = &http.Client{Transport: s.modisTransport(key, proxyURL)}
			keepIdleConnections = true
		} else {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			if proxyURL != nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
			client = &http.Client{Transport: transport}
		}
		if source == "cloudsat" {
			// THREDDS redirects through a session-backed authentication endpoint.
			// Keep its cookies within this fetch, never across accounts or routes.
			client.Jar, err = cookiejar.New(nil)
			if err != nil {
				client.CloseIdleConnections()
				return route{}, errors.New("CloudSat session could not be initialized")
			}
		}
		var authorizer Authorizer
		if kind != "" {
			cacheKey := fmt.Sprintf("%d:%s:%d:%s", credentialID, credentialVersion, proxyID, proxyVersion)
			s.mu.Lock()
			authorizer = s.authorizers[cacheKey]
			if authorizer == nil {
				authorizer, err = Build(kind, secret, client)
				if err == nil {
					if len(s.authorizers) >= 256 {
						clear(s.authorizers)
					}
					s.authorizers[cacheKey] = authorizer
				}
			}
			s.mu.Unlock()
			if err != nil {
				return route{}, err
			}
		}
		return route{accountID: account.ID, credentialID: credentialID, proxyID: proxyID, accountVersion: account.UpdatedAt, credentialVersion: credentialVersion, proxyVersion: proxyVersion, fingerprint: fingerprint, client: client, keepIdleConnections: keepIdleConnections, authorizer: authorizer, provider: era5.NewWithToken(client, secret.Token)}, nil
	}
	return route{}, ErrNoRoute
}

// The transport is intentionally long-lived while each HTTP client remains
// request-scoped (especially CheckRedirect). Idle sockets are closed on
// eviction, and active in-flight requests are not interrupted.
func (s *Service) modisTransport(key string, proxyURL *url.URL) *http.Transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.modisTransports[key]; existing != nil {
		return existing
	}
	if len(s.modisTransports) >= 32 {
		for oldKey, old := range s.modisTransports {
			old.CloseIdleConnections()
			delete(s.modisTransports, oldKey)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// LAADS granules are individually large transfers, not tiny RPCs.
	// HTTP/2 multiplexed all four workers onto one TCP congestion window in
	// production, slowing every download when that socket became congested.
	// HTTP/1.1 retains sequential keep-alive reuse but gives *concurrent*
	// transfers independent TCP congestion control and retransmission state.
	// ForceAttemptHTTP2=false alone is insufficient with inherited settings:
	// an explicit empty TLSNextProto map disables HTTP/2 negotiation.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.Proxy = nil
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 16
	transport.MaxConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	s.modisTransports[key] = transport
	return transport
}

func (s *Service) Submit(ctx context.Context, submission era5.Submission) (era5.Job, error) {
	route, err := s.resolve(ctx, "era5", nil)
	if err != nil {
		return era5.Job{}, err
	}
	defer route.client.CloseIdleConnections()
	s.Store.markUsed(ctx, route)
	route.client.Timeout = 60 * time.Second
	job, err := route.provider.Submit(ctx, submission)
	if err != nil {
		s.recordProviderOutcome(route, err)
		return era5.Job{}, err
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Store.SaveBinding(persistCtx, job.ID, Binding{AccountID: route.accountID, CredentialID: route.credentialID, TokenHash: route.fingerprint}); err != nil {
		return job, fmt.Errorf("%w: CDS accepted job %s but the centre route could not be saved", era5.ErrSubmissionUncertain, job.ID)
	}
	s.Store.markResult(context.WithoutCancel(ctx), route, true)
	return job, nil
}

func (s *Service) Process(ctx context.Context, collection string) (era5.Process, error) {
	route, err := s.resolve(ctx, "era5", nil)
	if err != nil {
		return era5.Process{}, err
	}
	defer route.client.CloseIdleConnections()
	route.client.Timeout = 60 * time.Second
	return route.provider.Process(ctx, collection)
}

func (s *Service) Job(ctx context.Context, id string) (era5.Job, error) {
	binding, err := s.Store.Binding(ctx, id)
	if err != nil {
		return era5.Job{}, err
	}
	route, err := s.resolve(ctx, "era5", &binding)
	if err != nil {
		return era5.Job{}, err
	}
	defer route.client.CloseIdleConnections()
	s.Store.markUsed(ctx, route)
	route.client.Timeout = 60 * time.Second
	job, err := route.provider.Job(ctx, id)
	s.recordProviderOutcome(route, err)
	return job, err
}

func (s *Service) Result(ctx context.Context, id string) (model.Asset, error) {
	binding, err := s.Store.Binding(ctx, id)
	if err != nil {
		return model.Asset{}, err
	}
	route, err := s.resolve(ctx, "era5", &binding)
	if err != nil {
		return model.Asset{}, err
	}
	defer route.client.CloseIdleConnections()
	s.Store.markUsed(ctx, route)
	route.client.Timeout = 60 * time.Second
	asset, err := route.provider.Result(ctx, id)
	s.recordProviderOutcome(route, err)
	return asset, err
}

func (s *Service) recordProviderOutcome(route route, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err == nil {
		s.Store.markResult(ctx, route, true)
		return
	}
	var response *era5.HTTPError
	if errors.As(err, &response) && (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) {
		s.Store.markResult(ctx, route, false)
	}
}

func proxyAllows(providers []string, source string) bool {
	if len(providers) == 0 {
		return true
	}
	for _, provider := range providers {
		if provider == "*" || provider == source {
			return true
		}
	}
	return false
}
