package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Authorizer interface {
	Authorize(context.Context, *http.Request) error
}

type Registry struct {
	items map[string]Authorizer
}

func NewRegistry(client *http.Client) *Registry {
	return &Registry{items: map[string]Authorizer{
		"laads":     bearer(os.Getenv("SATDOWN_LAADS_TOKEN"), "LAADS download token"),
		"earthdata": bearer(os.Getenv("SATDOWN_EARTHDATA_TOKEN"), "NASA Earthdata bearer token"),
		"earthcare": bearer(os.Getenv("SATDOWN_EARTHCARE_TOKEN"), "ESA MAAP access token"),
		"cloudsat":  &basic{username: strings.TrimSpace(os.Getenv("SATDOWN_CLOUDSAT_USERNAME")), password: os.Getenv("SATDOWN_CLOUDSAT_PASSWORD")},
		"eumetsat": &eumetsatToken{
			client: client,
			key:    strings.TrimSpace(os.Getenv("SATDOWN_EUMETSAT_CONSUMER_KEY")),
			secret: os.Getenv("SATDOWN_EUMETSAT_CONSUMER_SECRET"),
		},
	}}
}

func (r *Registry) Resolve(ref string) (Authorizer, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	item, ok := r.items[ref]
	if !ok {
		return nil, errors.New("unsupported auth reference")
	}
	return item, nil
}

func (r *Registry) Configured(ref string) bool {
	a, err := r.Resolve(ref)
	if err != nil {
		return false
	}
	if a == nil {
		return true
	}
	switch value := a.(type) {
	case *bearerAuth:
		return value.token != ""
	case *basic:
		return value.username != "" && value.password != ""
	case *eumetsatToken:
		return value.key != "" && value.secret != ""
	default:
		return false
	}
}

type bearerAuth struct {
	token string
	name  string
}

func bearer(token, name string) *bearerAuth {
	return &bearerAuth{token: strings.TrimSpace(token), name: name}
}

func (b *bearerAuth) Authorize(_ context.Context, req *http.Request) error {
	if b == nil || b.token == "" {
		return errors.New(b.name + " is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	if b.name == "LAADS download token" {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	return nil
}

type basic struct {
	username string
	password string
}

func (b *basic) Authorize(_ context.Context, req *http.Request) error {
	if b == nil || b.username == "" || b.password == "" {
		return errors.New("CloudSat THREDDS credentials are not configured")
	}
	req.SetBasicAuth(b.username, b.password)
	return nil
}

type eumetsatToken struct {
	client *http.Client
	key    string
	secret string

	mu        sync.Mutex
	access    string
	expiresAt time.Time
}

func (e *eumetsatToken) Authorize(ctx context.Context, req *http.Request) error {
	token, err := e.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (e *eumetsatToken) token(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.key == "" || e.secret == "" {
		return "", errors.New("EUMETSAT Consumer Key/Secret are not configured")
	}
	if e.access != "" && time.Until(e.expiresAt) > time.Minute {
		return e.access, nil
	}
	form := url.Values{
		"grant_type":      {"client_credentials"},
		"validity_period": {"86400"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.eumetsat.int/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(e.key, e.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := e.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", errors.New("EUMETSAT token request failed: " + resp.Status)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return "", errors.New("EUMETSAT token response is incomplete")
	}
	e.access = payload.AccessToken
	e.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return e.access, nil
}
