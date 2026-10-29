package authstore

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if _, err := s.VerifyPassword(t.Context(), "nonexistent-user", "fixture-long-password"); err == nil || err.Error() != "invalid credentials" {
		t.Fatalf("unexpected missing-user error: %v", err)
	}
	if _, err := s.VerifyPassword(t.Context(), "operator", "wrong-long-password"); err == nil || err.Error() != "invalid credentials" {
		t.Fatalf("unexpected wrong-password error: %v", err)
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
	if err := s.db.QueryRowContext(t.Context(), "SELECT secret_hash FROM satellite_api_keys WHERE id=?", key.ID).Scan(&stored); err != nil {
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

func TestSharedNSMCUsersDoNotShareKeysOrSessions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "shared.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	user, err := s.CreateUser(t.Context(), "shared-user", "fixture-long-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE api_keys(id TEXT PRIMARY KEY,user_id TEXT,secret_hash BLOB,revoked_at TEXT)`,
		`CREATE TABLE web_sessions(id TEXT PRIMARY KEY,user_id TEXT,secret_hash BLOB)`,
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	raw := bytes.Repeat([]byte{7}, secretBytes)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO api_keys(id,user_id,secret_hash) VALUES(?,?,?)", "legacy", user.ID, hashSecret(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO web_sessions(id,user_id,secret_hash) VALUES(?,?,?)", "legacy", user.ID, hashSecret(raw)); err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListAPIKeys(t.Context(), user.ID)
	if err != nil || len(keys) != 0 {
		t.Fatal("NSMC keys appeared in Satellite list")
	}
	// Relabeling an NSMC token cannot turn it into a Satellite identity.
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := s.AuthenticateAPIKey(t.Context(), "satellite_v1_legacy_"+encoded); err == nil {
		t.Fatal("NSMC key accepted with a rewritten prefix")
	}
	if _, err := s.VerifySession(t.Context(), "satellitesess_v1_legacy_"+encoded); err == nil {
		t.Fatal("NSMC session accepted with a rewritten prefix")
	}
	if err := s.RevokeAPIKey(t.Context(), user.ID, "legacy"); err == nil {
		t.Fatal("Satellite revoked an NSMC key")
	}
	session, err := s.CreateSession(t.Context(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL", "SELECT COUNT(*) FROM web_sessions"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), statement).Scan(&count); err != nil || count != 1 {
			t.Fatal("NSMC keys or sessions were modified")
		}
	}
}
