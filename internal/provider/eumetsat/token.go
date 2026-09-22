package eumetsat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const tokenEndpoint = apiBase + "/token"

type TokenManager struct {
	client *http.Client
	key    string
	secret string

	mu        sync.Mutex
	access    string
	expiresAt time.Time
}

func NewTokenManager(client *http.Client, consumerKey, consumerSecret string) *TokenManager {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &TokenManager{
		client: client,
		key:    consumerKey,
		secret: consumerSecret,
	}
}

func (m *TokenManager) Authorize(ctx context.Context, req *http.Request) error {
	token, err := m.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (m *TokenManager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(m.key) == "" || strings.TrimSpace(m.secret) == "" {
		return "", errors.New("EUMETSAT Consumer Key/Secret are not configured")
	}
	if m.access != "" && time.Until(m.expiresAt) > 30*time.Second {
		return m.access, nil
	}

	form := url.Values{
		"grant_type":      {"client_credentials"},
		"validity_period": {"86400"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(m.key, m.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
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
	m.access = payload.AccessToken
	m.expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	return m.access, nil
}
