package access

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/auth"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/vault"
)

func TestInspectEarthdataJWTExpiry(t *testing.T) {
	v, err := vault.Open(filepath.Join(t.TempDir(), "master.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2026, 11, 23, 12, 34, 56, 0, time.UTC)
	token := testJWT(t, map[string]any{"exp": exp.Unix(), "iss": "Earthdata Login"})
	plain, _ := json.Marshal(auth.Secret{Token: token})
	encrypted, err := v.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectCredentialExpiry("earthdata", encrypted, v)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "known" || info.ExpiresAt != exp.Format(time.RFC3339) || info.Source != "Earthdata Login token exp" {
		t.Fatalf("unexpected expiry: %#v", info)
	}
}

func TestInspectLAADSOpaqueTokenReportsOfficialFieldMissing(t *testing.T) {
	v, err := vault.Open(filepath.Join(t.TempDir(), "master.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(auth.Secret{Token: "opaque-laads-token"})
	encrypted, err := v.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectCredentialExpiry("laads", encrypted, v)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "not_provided" || info.ExpiresAt != "" || info.Source != "LAADS /oauth/key" {
		t.Fatalf("unexpected expiry: %#v", info)
	}
}

func TestInspectEarthCAREJWTExpiry(t *testing.T) {
	v, err := vault.Open(filepath.Join(t.TempDir(), "master.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	token := testJWT(t, map[string]any{"exp": exp.Unix(), "iss": "https://iam.maap.eo.esa.int/realms/esa-maap"})
	plain, _ := json.Marshal(auth.Secret{Token: token})
	encrypted, err := v.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectCredentialExpiry("earthcare", encrypted, v)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "known" || info.ExpiresAt != exp.Format(time.RFC3339) {
		t.Fatalf("unexpected expiry: %#v", info)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
