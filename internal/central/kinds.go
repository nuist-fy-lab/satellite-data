package central

import (
	"errors"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/sourceaccess"
)

func ProviderKind(source string) (string, error) {
	route, err := accessRoute(source)
	return route.DefaultCredentialKind(), err
}

func accessRoute(source string) (sourceaccess.Route, error) {
	route, ok := sourceaccess.ForRoute(source)
	if !ok {
		return route, errors.New("unknown data source")
	}
	return route, nil
}

func ValidateSecret(kind string, secret Secret) error {
	switch kind {
	case "cds", "laads", "earthdata", "earthcare":
		token := strings.TrimSpace(secret.Token)
		if token == "" || len(token) > 65536 {
			return errors.New("token is required")
		}
		for _, c := range token {
			if c <= 32 || c >= 127 {
				return errors.New("invalid token")
			}
		}
	case "eumetsat":
		if strings.TrimSpace(secret.ConsumerKey) == "" || secret.ConsumerSecret == "" {
			return errors.New("Consumer Key and Consumer Secret are required")
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
