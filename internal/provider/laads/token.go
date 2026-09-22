package laads

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type DownloadToken struct {
	value string
}

func NewDownloadToken(value string) *DownloadToken {
	return &DownloadToken{value: strings.TrimSpace(value)}
}

func (t *DownloadToken) Authorize(_ context.Context, req *http.Request) error {
	if t == nil || t.value == "" {
		return errors.New("LAADS Download Token is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+t.value)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	return nil
}
