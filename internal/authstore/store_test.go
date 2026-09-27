package authstore

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestSatelliteAPIKeysAreHashedAndOwnerRevocable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	user, err := s.CreateUser(t.Context(), "operator", "fixture-long-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := s.CreateAPIKey(t.Context(), user.ID, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	id, raw, err := parseToken(secret, "satellite_v1")
	if err != nil || id != key.ID || strings.Contains(secret, "nsmc_v1_") {
		t.Fatal("invalid Satellite key namespace")
	}
	var stored []byte
	if err := s.db.QueryRowContext(t.Context(), "SELECT secret_hash FROM api_keys WHERE id=?", key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, raw) || !bytes.Equal(stored, hashSecret(raw)) || strings.Contains(key.Prefix, strings.SplitN(secret, "_", 4)[3]) {
		t.Fatal("store contains raw key material")
	}
	if _, err := s.AuthenticateAPIKey(t.Context(), strings.Replace(secret, "satellite_v1_", "nsmc_v1_", 1)); err == nil {
		t.Fatal("NSMC key namespace accepted")
	}
	if err := s.RevokeAPIKey(t.Context(), "another-owner", key.ID); err == nil {
		t.Fatal("other owner revoked key")
	}
	if _, err := s.AuthenticateAPIKey(t.Context(), secret); err != nil {
		t.Fatal("valid key rejected")
	}
	keys, err := s.ListAPIKeys(t.Context(), user.ID)
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Fatal("key usage not tracked")
	}
	if err := s.RevokeAPIKey(t.Context(), user.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAPIKey(t.Context(), secret); err == nil {
		t.Fatal("revoked key accepted")
	}
}
