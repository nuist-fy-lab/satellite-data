package earthcare

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type AccessToken struct {
	value string
}

func NewAccessToken(value string) *AccessToken {
	return &AccessToken{value: strings.TrimSpace(value)}
}

func (t *AccessToken) Authorize(_ context.Context, req *http.Request) error {
	if t == nil || t.value == "" {
		return errors.New("ESA MAAP access token is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+t.value)
	return nil
}
