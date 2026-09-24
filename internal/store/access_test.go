package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAccessPools(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()

	credential, err := state.UpsertCredential(ctx, 0, "LAADS main", "laads", "2026-12-31", []byte("ciphertext"), "••••1234", true, 20)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := state.UpsertProxy(ctx, 0, "proxy-a", []byte("ciphertext"), "socks5", "127.0.0.1:1080", []string{"modis", "viirs"}, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	account, err := state.UpsertAccount(ctx, 0, "MODIS main", "modis", &credential.ID, &proxy.ID, "fixed", true, 30)
	if err != nil {
		t.Fatal(err)
	}
	if account.CredentialAlias != "LAADS main" || account.ProxyAlias != "proxy-a" {
		t.Fatalf("account joins not populated: %#v", account)
	}
	if credential.ExpiresAt != "2026-12-31" {
		t.Fatalf("expires_at=%q", credential.ExpiresAt)
	}
	accounts, err := state.AccountsForSource(ctx, "modis")
	if err != nil || len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("accounts=%#v err=%v", accounts, err)
	}
	proxies, err := state.EligibleProxies(ctx, "cloudsat")
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 0 {
		t.Fatalf("provider-scoped proxy leaked into cloudsat: %#v", proxies)
	}
	state.MarkAccountAuthFailure(ctx, account.ID, &credential.ID, "rejected")
	accounts, err = state.AccountsForSource(ctx, "modis")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("auth-required account remained schedulable: %#v", accounts)
	}
}

func TestAccessSchemaMigratesCredentialExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE credentials (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  alias TEXT NOT NULL UNIQUE COLLATE NOCASE,
		  kind TEXT NOT NULL,
		  encrypted_secret BLOB NOT NULL,
		  secret_hint TEXT NOT NULL DEFAULT '',
		  enabled INTEGER NOT NULL DEFAULT 1,
		  priority INTEGER NOT NULL DEFAULT 0,
		  state TEXT NOT NULL DEFAULT 'ready',
		  last_error TEXT NOT NULL DEFAULT '',
		  last_used_at TEXT NOT NULL DEFAULT '',
		  last_success_at TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL,
		  updated_at TEXT NOT NULL
		);
		INSERT INTO credentials(alias,kind,encrypted_secret,secret_hint,created_at,updated_at)
		VALUES('legacy','laads',x'01','••••0001','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	items, err := state.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Alias != "legacy" || items[0].ExpiresAt != "" {
		t.Fatalf("unexpected migrated credentials: %#v", items)
	}
}
