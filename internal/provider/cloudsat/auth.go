package cloudsat

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type BasicAuth struct {
	username string
	password string
}

func NewBasicAuth(username, password string) *BasicAuth {
	return &BasicAuth{
		username: strings.TrimSpace(username),
		password: password,
	}
}

func (a *BasicAuth) Authorize(_ context.Context, req *http.Request) error {
	if a == nil || a.username == "" || a.password == "" {
		return errors.New("CloudSat THREDDS credentials are not configured")
	}
	req.SetBasicAuth(a.username, a.password)
	return nil
}
