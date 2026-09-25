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

type Secret struct {
	Token          string `json:"token,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	ConsumerKey    string `json:"consumer_key,omitempty"`
	ConsumerSecret string `json:"consumer_secret,omitempty"`
}

func Build(kind string, secret Secret, client *http.Client) (Authorizer, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "laads":
		return bearer(secret.Token, "LAADS download token"), nil
	case "earthdata":
		return bearer(secret.Token, "NASA Earthdata bearer token"), nil
	case "earthcare":
		return &earthcareToken{client: client, offline: strings.TrimSpace(secret.Token)}, nil
	case "cloudsat":
		return &basic{username: strings.TrimSpace(secret.Username), password: secret.Password}, nil
	case "eumetsat":
		return &eumetsatToken{client: client, key: strings.TrimSpace(secret.ConsumerKey), secret: secret.ConsumerSecret}, nil
	default:
		return nil, errors.New("unsupported credential kind")
	}
}

type CredentialError struct {
	Detail string
}

func (e *CredentialError) Error() string { return e.Detail }

func IsCredentialError(err error) bool {
	var target *CredentialError
	return errors.As(err, &target)
}

type Registry struct {
	items map[string]Authorizer
}

func NewRegistry(client *http.Client) *Registry {
	return &Registry{items: map[string]Authorizer{
		"laads":     bearer(os.Getenv("SATDOWN_LAADS_TOKEN"), "LAADS download token"),
		"earthdata": bearer(os.Getenv("SATDOWN_EARTHDATA_TOKEN"), "NASA Earthdata bearer token"),
		"earthcare": &earthcareToken{client: client, offline: strings.TrimSpace(os.Getenv("SATDOWN_EARTHCARE_TOKEN"))},
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
	case *earthcareToken:
		return value.offline != ""
	default:
		return false
	}
}

const (
	earthcareTokenEndpoint = "https://iam.maap.eo.esa.int/realms/esa-maap/protocol/openid-connect/token"
	earthcareClientID      = "offline-token"
	earthcareClientSecret  = "p1eL7uonXs6MDxtGbgKdPVRAmnGxHpVE"
)

type earthcareToken struct {
	client  *http.Client
	offline string

	mu        sync.Mutex
	access    string
	expiresAt time.Time
}

func (e *earthcareToken) Authorize(ctx context.Context, req *http.Request) error {
	token, err := e.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (e *earthcareToken) token(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.offline == "" {
		return "", &CredentialError{Detail: "ESA MAAP offline token is not configured"}
	}
	if e.access != "" && time.Until(e.expiresAt) > time.Minute {
		return e.access, nil
	}
	form := url.Values{
		"client_id":     {earthcareClientID},
		"client_secret": {earthcareClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {e.offline},
		"scope":         {"offline_access openid"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, earthcareTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
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
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest {
			return "", &CredentialError{Detail: "ESA MAAP offline token was rejected"}
		}
		return "", errors.New("ESA MAAP token exchange failed: " + resp.Status)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return "", errors.New("ESA MAAP token exchange response is incomplete")
	}
	e.access = payload.AccessToken
	e.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return e.access, nil
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
		name := "bearer token"
		if b != nil && b.name != "" {
			name = b.name
		}
		return &CredentialError{Detail: name + " is not configured"}
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
		return &CredentialError{Detail: "CloudSat THREDDS credentials are not configured"}
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
		return "", &CredentialError{Detail: "EUMETSAT Consumer Key/Secret are not configured"}
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
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", &CredentialError{Detail: "EUMETSAT credentials were rejected"}
		}
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
