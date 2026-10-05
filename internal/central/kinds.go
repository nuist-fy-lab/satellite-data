package central

import (
	"errors"
	"strings"
)

func ProviderKind(source string) (string, error) {
	switch source {
	case "goes", "himawari8", "himawari9":
		return "", nil
	case "modis", "viirs":
		return "laads", nil
	case "mtg":
		return "eumetsat", nil
	case "earthcare":
		return "earthcare", nil
	case "calipso":
		return "earthdata", nil
	case "cloudsat":
		return "cloudsat", nil
	case "era5":
		return "cds", nil
	default:
		return "", errors.New("unknown provider")
	}
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
