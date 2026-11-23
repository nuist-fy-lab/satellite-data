package central

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
	"github.com/nuist-fy-lab/satellite-data/internal/egress"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

var ErrNoRoute = errors.New("no usable central account; configure this provider in Access Credentials")
var ErrChangedCredential = errors.New("CDS job credential changed; restore the original credential before querying this job")

type Service struct {
	Store         *Store
	clientFactory func(*url.URL) *http.Client
	egress        *egress.Pool
	mu            sync.Mutex
	authorizers   map[string]Authorizer
	// Each pool belongs to one MODIS account, credential revision and network
	// route. Never share authentication cookies or connections across routes.
	modisPools map[string]*modisPool
}

func (s *Service) DownloadEgressStatus() []egress.Status {
	return s.egress.Snapshot()
}

// ConfigureDownloadEgress selects the optional, download-only SSH SOCKS5
// pool. Provider discovery and CDS job control retain their saved account
// routes; each fetch acquires its own pinned egress until streaming finishes.
func (s *Service) ConfigureDownloadEgress(path string) error {
	cfg, err := egress.Load(path)
	if err != nil {
		return err
	}
	s.egress = egress.NewPool(cfg)
	return nil
}

// Each concurrent LAADS fetch leases one HTTP/2 transport for its entire
// lifetime. HTTP/2 is compatible with LAADS; exclusive transport leases
// prevent simultaneous granules from sharing a TCP congestion window.
type modisPool struct {
	idle chan *http.Transport
	all  []*http.Transport
}

func NewService(store *Store, clients ...*http.Client) *Service {
	s := &Service{Store: store, authorizers: make(map[string]Authorizer), modisPools: make(map[string]*modisPool)}
	if len(clients) > 0 && clients[0] != nil {
		s.clientFactory = func(*url.URL) *http.Client { copy := *clients[0]; return &copy }
	}
	return s
}

type route struct {
	accountID, credentialID, proxyID                             int64
	accountVersion, credentialVersion, proxyVersion, fingerprint string
	credentialKind                                               string
	client                                                       *http.Client
	keepIdleConnections                                          bool
	release                                                      func()
	provider                                                     *era5.Provider
	authorizer                                                   Authorizer
}

func (s *Service) resolve(ctx context.Context, source string, binding *Binding) (route, error) {
	return s.resolveRoute(ctx, source, binding, false, nil, "")
}

func (s *Service) resolveFetch(ctx context.Context, source string, binding *Binding, proxy *url.URL, host string) (route, error) {
	return s.resolveRoute(ctx, source, binding, false, proxy, host)
}

// Route readiness probes check credentials and routing without acquiring an
// HTTP/2 transfer lease. Status requests must remain responsive even if all
// eight MODIS connections are occupied by long-running scientific downloads.
func (s *Service) probeRoute(ctx context.Context, source string) (route, error) {
	return s.resolveRoute(ctx, source, nil, true, nil, "")
}

func (s *Service) resolveRoute(ctx context.Context, source string, binding *Binding, probeOnly bool, egressProxy *url.URL, host string) (route, error) {
	access, err := accessRoute(source)
	if err != nil {
		return route{}, err
	}
	kind := access.DefaultCredentialKind()
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
	credentials := make(map[int64]downloader.Credential, len(pools.Credentials))
	for _, credential := range pools.Credentials {
		credentials[credential.ID] = credential
	}
	if len(access.CredentialKinds) > 1 {
		preferredKind := access.PreferredCredentialKind(host)
		isPreferred := func(account downloader.Account) bool {
			return account.CredentialID != nil && credentials[*account.CredentialID].Kind == preferredKind
		}
		// Preserve priority and rotation among accounts using the same credential kind.
		sort.SliceStable(pools.Accounts, func(i, j int) bool {
			return isPreferred(pools.Accounts[i]) && !isPreferred(pools.Accounts[j])
		})
	}
	for _, account := range pools.Accounts {
		if account.SourceID != source || !account.Enabled || account.State == "auth_required" || (kind != "" && account.CredentialID == nil) {
			continue
		}
		if binding != nil && (account.ID != binding.AccountID || account.CredentialID == nil || *account.CredentialID != binding.CredentialID) {
			continue
		}
		var plain []byte
		var secret Secret
		var credential downloader.Credential
		if account.CredentialID != nil {
			credential = credentials[*account.CredentialID]
			if !access.AcceptsCredential(credential.Kind) || !credential.Enabled || credential.State == "auth_required" {
				continue
			}
			if kind == "laads" && credential.Kind == "earthdata" {
				expiry, parseErr := time.Parse(time.RFC3339, credential.ExpiresAt)
				if parseErr == nil && !expiry.After(time.Now()) {
					continue
				}
			}
			plain, err = s.Store.readSecret(ctx, "credentials", credential.ID)
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
		// The account proxy still governs credential discovery and CDS control.
		// An explicit download route changes only this fetch's HTTP transport.
		if egressProxy != nil {
			proxyURL = egressProxy
		}
		transportIdentity := ""
		if proxyURL != nil {
			transportIdentity = proxyURL.String()
		}
		var client *http.Client
		var keepIdleConnections bool
		var release func()
		if s.clientFactory != nil {
			client = s.clientFactory(proxyURL)
		} else if source == "modis" && probeOnly {
			client = &http.Client{}
		} else if source == "modis" {
			// LAADS serves many short granules through the same HTTPS endpoint.
			// Reuse TLS/TCP connections without sharing a cookie jar or credentials
			// between different user accounts, secret revisions or proxy routes.
			key := fmt.Sprintf("%s:%d:%s:%d:%s:%d:%s", source, account.ID, account.UpdatedAt,
				credential.ID, fingerprint, proxyID, proxyVersion+":"+transportIdentity)
			transport, returnToPool, leaseErr := s.leaseMODISTransport(ctx, key, proxyURL)
			if leaseErr != nil {
				return route{}, leaseErr
			}
			client = &http.Client{Transport: transport}
			release = returnToPool
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
		if credential.Kind != "" {
			cacheKey := fmt.Sprintf("%d:%s:%d:%s:%s", credential.ID, credential.UpdatedAt, proxyID, proxyVersion, transportIdentity)
			s.mu.Lock()
			authorizer = s.authorizers[cacheKey]
			if authorizer == nil {
				authorizer, err = Build(credential.Kind, secret, client)
				if err == nil {
					if len(s.authorizers) >= 256 {
						clear(s.authorizers)
					}
					s.authorizers[cacheKey] = authorizer
				}
			}
			s.mu.Unlock()
			if err != nil {
				if release != nil {
					release()
				}
				return route{}, err
			}
		}
		return route{accountID: account.ID, credentialID: credential.ID, proxyID: proxyID, accountVersion: account.UpdatedAt, credentialVersion: credential.UpdatedAt, proxyVersion: proxyVersion, fingerprint: fingerprint, credentialKind: credential.Kind, client: client, keepIdleConnections: keepIdleConnections, release: release, authorizer: authorizer, provider: era5.NewWithToken(client, secret.Token)}, nil
	}
	return route{}, ErrNoRoute
}

// A pool retains at most eight independent HTTP/2 TCP connections per
// identity/route, matching the downloaders' 4→8 adaptive concurrency ceiling.
// One lease is held until the response body finishes streaming. Consequently
// no two simultaneous granules multiplex onto one TCP socket, while an idle
// connection can be reused by the next granule.
func (s *Service) leaseMODISTransport(ctx context.Context, key string, proxyURL *url.URL) (*http.Transport, func(), error) {
	s.mu.Lock()
	pool := s.modisPools[key]
	if pool == nil {
		if len(s.modisPools) >= 32 {
			for oldKey, old := range s.modisPools {
				for _, transport := range old.all {
					transport.CloseIdleConnections()
				}
				delete(s.modisPools, oldKey)
			}
		}
		pool = &modisPool{idle: make(chan *http.Transport, 8)}
		for range cap(pool.idle) {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			if proxyURL != nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
			transport.ForceAttemptHTTP2 = true
			transport.MaxConnsPerHost = 1
			transport.MaxIdleConnsPerHost = 1
			transport.IdleConnTimeout = 90 * time.Second
			pool.all = append(pool.all, transport)
			pool.idle <- transport
		}
		s.modisPools[key] = pool
	}
	s.mu.Unlock()
	select {
	case transport := <-pool.idle:
		return transport, func() { pool.idle <- transport }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
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
