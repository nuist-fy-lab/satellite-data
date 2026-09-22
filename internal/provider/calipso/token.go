package calipso

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type EarthdataToken struct {
	value string
}

func NewEarthdataToken(value string) *EarthdataToken {
	return &EarthdataToken{value: strings.TrimSpace(value)}
}

func (t *EarthdataToken) Authorize(_ context.Context, req *http.Request) error {
	if t == nil || t.value == "" {
		return errors.New("Earthdata bearer token is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+t.value)
	return nil
}
