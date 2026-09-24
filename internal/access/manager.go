package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/store"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/transfer"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/vault"
)

var ErrNoRoute = errors.New("no usable access route")

type Route struct {
	AccountID    int64
	CredentialID *int64
	ProxyID      *int64
	Authorizer   auth.Authorizer
	Transfer     *transfer.HTTP
	Legacy       bool
}

type cachedAuthorizer struct {
	updatedAt  string
	authorizer auth.Authorizer
}

type Manager struct {
	store  *store.Store
	vault  *vault.Vault
	legacy *auth.Registry

	mu          sync.Mutex
	authorizers map[int64]cachedAuthorizer
}

func NewManager(state *store.Store, secretVault *vault.Vault, legacy *auth.Registry) (*Manager, error) {
	if state == nil || secretVault == nil || legacy == nil {
		return nil, errors.New("access manager requires store, vault and legacy registry")
	}
	return &Manager{
		store: state, vault: secretVault, legacy: legacy,
		authorizers: make(map[int64]cachedAuthorizer),
	}, nil
}

func (m *Manager) Resolve(ctx context.Context, sourceID, authRef string, timeout time.Duration) (Route, error) {
	accounts, err := m.store.AccountsForSource(ctx, sourceID)
	if err != nil {
		return Route{}, err
	}
	for _, account := range accounts {
		route, ok, err := m.routeForAccount(ctx, account, authRef, timeout)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		_ = m.store.MarkAccountUsed(ctx, account.ID, route.ProxyID)
		return route, nil
	}

	// Backward compatibility: environment-file credentials remain usable until
	// the web-managed pool has a route for this provider.
	if len(accounts) == 0 {
		authorizer, err := m.legacy.Resolve(authRef)
		if err != nil {
			return Route{}, err
		}
		if authRef != "" && !m.legacy.Configured(authRef) {
			return Route{}, ErrNoRoute
		}
		transport, err := newTransfer(timeout, nil)
		if err != nil {
			return Route{}, err
		}
		return Route{
			Authorizer: authorizer,
			Transfer:   transport,
			Legacy:     true,
		}, nil
	}
	return Route{}, ErrNoRoute
}

func (m *Manager) HasUsableRoute(ctx context.Context, sourceID, authRef string, timeout time.Duration) bool {
	route, err := m.Resolve(ctx, sourceID, authRef, timeout)
	return err == nil && route.Transfer != nil
}

func (m *Manager) Configured(ctx context.Context, sourceID, authRef string) bool {
	if authRef == "" {
		return true
	}
	accounts, err := m.store.AccountsForSource(ctx, sourceID)
	if err == nil {
		for _, account := range accounts {
			if account.CredentialID == nil {
				continue
			}
			credential, err := m.store.Credential(ctx, *account.CredentialID)
			if err == nil && credential.Enabled && credential.State != "auth_required" && credential.Kind == authRef {
				return true
			}
		}
	}
	return m.legacy.Configured(authRef)
}

func (m *Manager) routeForAccount(ctx context.Context, account model.Account, authRef string, timeout time.Duration) (Route, bool, error) {
	var authorizer auth.Authorizer
	var credentialID *int64
	if authRef != "" {
		if account.CredentialID == nil {
			return Route{}, false, nil
		}
		credential, err := m.store.Credential(ctx, *account.CredentialID)
		if err != nil || !credential.Enabled || credential.State == "auth_required" || credential.Kind != authRef {
			return Route{}, false, nil
		}
		authorizer, err = m.authorizer(ctx, credential)
		if err != nil {
			return Route{}, false, err
		}
		value := credential.ID
		credentialID = &value
	}

	var proxyID *int64
	var proxyURL *url.URL
	switch account.ProxyMode {
	case "direct":
	case "fixed":
		if account.ProxyID == nil {
			return Route{}, false, nil
		}
		item, err := m.store.Proxy(ctx, *account.ProxyID)
		if err != nil || !item.Enabled || item.State == "disabled" || !proxyAllows(item.Providers, account.SourceID) {
			return Route{}, false, nil
		}
		proxyURL, err = m.proxyURL(ctx, item.ID)
		if err != nil {
			return Route{}, false, err
		}
		value := item.ID
		proxyID = &value
	case "auto":
		items, err := m.store.EligibleProxies(ctx, account.SourceID)
		if err != nil {
			return Route{}, false, err
		}
		sort.SliceStable(items, func(i, j int) bool {
			if (items[i].State == "ready") != (items[j].State == "ready") {
				return items[i].State == "ready"
			}
			if items[i].Priority != items[j].Priority {
				return items[i].Priority > items[j].Priority
			}
			if (items[i].LastUsedAt == "") != (items[j].LastUsedAt == "") {
				return items[i].LastUsedAt == ""
			}
			return items[i].LastUsedAt < items[j].LastUsedAt
		})
		if len(items) == 0 {
			return Route{}, false, nil
		}
		proxyURL, err = m.proxyURL(ctx, items[0].ID)
		if err != nil {
			return Route{}, false, err
		}
		value := items[0].ID
		proxyID = &value
	default:
		return Route{}, false, errors.New("invalid proxy mode")
	}

	transport, err := newTransfer(timeout, proxyURL)
	if err != nil {
		return Route{}, false, err
	}
	return Route{
		AccountID: account.ID, CredentialID: credentialID, ProxyID: proxyID,
		Authorizer: authorizer, Transfer: transport,
	}, true, nil
}

func (m *Manager) authorizer(ctx context.Context, credential model.Credential) (auth.Authorizer, error) {
	m.mu.Lock()
	if cached, ok := m.authorizers[credential.ID]; ok && cached.updatedAt == credential.UpdatedAt {
		m.mu.Unlock()
		return cached.authorizer, nil
	}
	m.mu.Unlock()

	encrypted, kind, err := m.store.CredentialSecret(ctx, credential.ID)
	if err != nil {
		return nil, err
	}
	plain, err := m.vault.Open(encrypted)
	if err != nil {
		return nil, err
	}
	var secret auth.Secret
	if err := json.Unmarshal(plain, &secret); err != nil {
		return nil, err
	}
	authorizer, err := auth.Build(kind, secret, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.authorizers[credential.ID] = cachedAuthorizer{updatedAt: credential.UpdatedAt, authorizer: authorizer}
	m.mu.Unlock()
	return authorizer, nil
}

func (m *Manager) proxyURL(ctx context.Context, id int64) (*url.URL, error) {
	encrypted, err := m.store.ProxySecret(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, err := m.vault.Open(encrypted)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(string(plain))
	if err != nil {
		return nil, err
	}
	if parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5") {
		return nil, errors.New("invalid stored proxy URL")
	}
	return parsed, nil
}

func (m *Manager) MarkSuccess(ctx context.Context, route Route) {
	if route.Legacy || route.AccountID <= 0 {
		return
	}
	m.store.MarkRouteSuccess(ctx, route.AccountID, route.CredentialID, route.ProxyID)
}

func (m *Manager) MarkAuthFailure(ctx context.Context, route Route, detail string) {
	if route.Legacy || route.AccountID <= 0 {
		return
	}
	m.store.MarkAccountAuthFailure(ctx, route.AccountID, route.CredentialID, detail)
}

func (m *Manager) MarkProxyFailure(ctx context.Context, route Route, detail string) {
	if route.ProxyID == nil {
		return
	}
	m.store.MarkProxyFailure(ctx, *route.ProxyID, detail)
}

func ValidateSecret(kind string, secret auth.Secret) error {
	switch kind {
	case "laads", "earthdata", "earthcare":
		if strings.TrimSpace(secret.Token) == "" {
			return errors.New("token is required")
		}
	case "eumetsat":
		if strings.TrimSpace(secret.ConsumerKey) == "" || secret.ConsumerSecret == "" {
			return errors.New("consumer_key and consumer_secret are required")
		}
	case "cloudsat":
		if strings.TrimSpace(secret.Username) == "" || secret.Password == "" {
			return errors.New("username and password are required")
		}
	default:
		return errors.New("unsupported credential kind")
	}
	return nil
}

func SecretHint(kind string, secret auth.Secret) string {
	mask := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) <= 4 {
			return "••••"
		}
		return "••••" + value[len(value)-4:]
	}
	switch kind {
	case "laads", "earthdata", "earthcare":
		return mask(secret.Token)
	case "eumetsat":
		return mask(secret.ConsumerKey)
	case "cloudsat":
		return strings.TrimSpace(secret.Username)
	default:
		return ""
	}
}

func ParseProxy(raw string) (*url.URL, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, "", err
	}
	if parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5") {
		return nil, "", errors.New("proxy URL must use http, https or socks5")
	}
	hint := net.JoinHostPort(parsed.Hostname(), parsed.Port())
	if parsed.Port() == "" {
		hint = parsed.Hostname()
	}
	return parsed, hint, nil
}

func newTransfer(timeout time.Duration, proxyURL *url.URL) (*transfer.HTTP, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConns = 64
	base.MaxIdleConnsPerHost = 16
	base.IdleConnTimeout = 90 * time.Second
	if proxyURL != nil {
		switch proxyURL.Scheme {
		case "http", "https":
			base.Proxy = http.ProxyURL(proxyURL)
		case "socks5":
			var proxyAuth *xproxy.Auth
			if proxyURL.User != nil {
				password, _ := proxyURL.User.Password()
				proxyAuth = &xproxy.Auth{User: proxyURL.User.Username(), Password: password}
			}
			dialer, err := xproxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, xproxy.Direct)
			if err != nil {
				return nil, err
			}
			base.Proxy = nil
			base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				type contextDialer interface {
					DialContext(context.Context, string, string) (net.Conn, error)
				}
				if cd, ok := dialer.(contextDialer); ok {
					return cd.DialContext(ctx, network, address)
				}
				return dialer.Dial(network, address)
			}
		}
	}
	return transfer.NewHTTP(&http.Client{Transport: base, Timeout: timeout}), nil
}

func proxyAllows(values []string, sourceID string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == "*" || value == sourceID {
			return true
		}
	}
	return false
}

func ProviderKind(sourceID string) (string, error) {
	switch sourceID {
	case "goes", "himawari8", "himawari9":
		return "", nil
	case "modis", "viirs":
		return "laads", nil
	case "mtg":
		return "eumetsat", nil
	case "earthcare":
		return "earthcare", nil
	case "calipso":
		return "earthdata", nil
	case "cloudsat":
		return "cloudsat", nil
	default:
		return "", fmt.Errorf("unknown provider %q", sourceID)
	}
}
