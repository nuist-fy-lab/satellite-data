package central

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	expiryKnown       = "known"
	expiryNotProvided = "not_provided"
	expiryCheckFailed = "check_failed"
)

type CredentialExpiry struct{ Status, CheckedAt, Source, ExpiresAt string }

func InspectCredentialExpiry(kind string, encrypted []byte, secretVault *Vault) (CredentialExpiry, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	info := CredentialExpiry{Status: expiryNotProvided, CheckedAt: now}
	if secretVault == nil {
		info.Status = expiryCheckFailed
		return info, errors.New("credential vault is unavailable")
	}
	plain, err := secretVault.Open(encrypted)
	if err != nil {
		info.Status = expiryCheckFailed
		return info, err
	}
	var secret Secret
	if err := json.Unmarshal(plain, &secret); err != nil {
		info.Status = expiryCheckFailed
		return info, err
	}

	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "cds":
		info.Source = "CDS Personal Access Token"
	case "earthdata":
		info.Source = "Earthdata Login token exp"
		if expiry, ok := jwtExpiry(secret.Token); ok {
			info.Status = expiryKnown
			info.ExpiresAt = expiry.Format(time.RFC3339)
		}
	case "earthcare":
		info.Source = "ESA MAAP offline token exp"
		if expiry, ok := jwtExpiry(secret.Token); ok {
			info.Status = expiryKnown
			info.ExpiresAt = expiry.Format(time.RFC3339)
		}
	case "laads":
		// Current LAADS web code obtains the download key from POST /oauth/key.
		// That endpoint returns only the opaque token text and exposes no expiry
		// field. If NASA changes the token to a JWT, honor an official exp claim.
		info.Source = "LAADS /oauth/key"
		if expiry, ok := jwtExpiry(secret.Token); ok {
			info.Status = expiryKnown
			info.ExpiresAt = expiry.Format(time.RFC3339)
			info.Source = "LAADS token exp"
		}
	case "eumetsat":
		// The stored credential is Consumer Key/Secret. The short-lived access
		// token minted from it has an expiry, but that is not the credential's
		// expiry and must not be presented as such.
		info.Source = "EUMETSAT Consumer Key/Secret"
	case "cloudsat":
		info.Source = "CloudSat DPC THREDDS credential"
	default:
		info.Status = expiryCheckFailed
		return info, fmt.Errorf("unsupported credential kind %q", kind)
	}
	return info, nil
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		padding := strings.Repeat("=", (4-len(parts[1])%4)%4)
		payload, err = base64.URLEncoding.DecodeString(parts[1] + padding)
		if err != nil {
			return time.Time{}, false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil {
		return time.Time{}, false
	}
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}
	var seconds int64
	switch value := raw.(type) {
	case json.Number:
		seconds, err = value.Int64()
		if err != nil {
			f, floatErr := strconv.ParseFloat(value.String(), 64)
			if floatErr != nil {
				return time.Time{}, false
			}
			seconds = int64(f)
		}
	case float64:
		seconds = int64(value)
	default:
		return time.Time{}, false
	}
	if seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}
