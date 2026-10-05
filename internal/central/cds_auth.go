package central

import (
	"context"
	"net/http"
)

type cdsAuthorizer struct{ token string }

func (c *cdsAuthorizer) Authorize(_ context.Context, req *http.Request) error {
	if c.token == "" {
		return ErrNoRoute
	}
	if req.URL.Scheme != "https" || req.URL.Host != "cds.climate.copernicus.eu" {
		return &CredentialError{Detail: "CDS credential is restricted to the official API"}
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	return nil
}
