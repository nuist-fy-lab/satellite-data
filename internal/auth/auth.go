package auth

import (
	"context"
	"net/http"
)

// Authorizer is the transfer interface; authentication lives in the centre.
type Authorizer interface {
	Authorize(context.Context, *http.Request) error
}
