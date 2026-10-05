package central

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const legacyTestSchema = `
CREATE TABLE credentials(id INTEGER PRIMARY KEY,alias TEXT,kind TEXT,encrypted_secret BLOB,enabled INTEGER,priority INTEGER,state TEXT,last_error TEXT,last_used_at TEXT,last_success_at TEXT,created_at TEXT,updated_at TEXT);
CREATE TABLE proxies(id INTEGER PRIMARY KEY,alias TEXT,encrypted_url BLOB,scheme TEXT,endpoint_hint TEXT,providers_json TEXT,enabled INTEGER,priority INTEGER,state TEXT,last_error TEXT,last_used_at TEXT,last_success_at TEXT,created_at TEXT,updated_at TEXT);
CREATE TABLE accounts(id INTEGER PRIMARY KEY,alias TEXT,source_id TEXT,credential_id INTEGER,proxy_id INTEGER,proxy_mode TEXT,enabled INTEGER,priority INTEGER,state TEXT,last_error TEXT,last_used_at TEXT,last_success_at TEXT,created_at TEXT,updated_at TEXT);
`

func legacyFixture(t *testing.T, corrupt bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.key")
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(plain []byte) []byte {
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		return append(append([]byte("v1:"), nonce...), aead.Seal(nil, nonce, plain, []byte("satellite-data-downloader-v1"))...)
	}
	database := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(legacyTestSchema); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(Secret{Token: "fixture-migrated-token"})
	if _, err := db.Exec(`INSERT INTO credentials VALUES(7,'original-credential','laads',?,1,9,'ready','','','','','original-version')`, seal(plain)); err != nil {
		t.Fatal(err)
	}
	if corrupt {
		if _, err := db.Exec(`INSERT INTO credentials VALUES(8,'broken','laads',?,1,0,'ready','','','','','original-version')`, []byte("bad")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO proxies VALUES(13,'original-proxy',?,'socks5','proxy.example:1080','["modis"]',1,5,'ready','','','','','original-version')`, seal([]byte("socks5://fixture-user:fixture-pass@proxy.example:1080"))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES(17,'original-account','modis',7,13,'fixed',1,3,'ready','','','','','original-version')`); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func TestLegacyMigrationPreservesIDsReencryptsAndRefusesOverwrite(t *testing.T) {
	s := testStore(t)
	database, key := legacyFixture(t, false)
	counts, err := s.ImportLegacy(t.Context(), database, key)
	if err != nil || counts != (ImportCounts{Credentials: 1, Proxies: 1, Accounts: 1}) {
		t.Fatal("migration failed", err)
	}
	pools, err := s.Pools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pools.Credentials[0].ID != 7 || pools.Proxies[0].ID != 13 || pools.Accounts[0].ID != 17 || *pools.Accounts[0].CredentialID != 7 || *pools.Accounts[0].ProxyID != 13 {
		t.Fatal("migration changed IDs or bindings")
	}
	plain, err := s.readSecret(t.Context(), "credentials", 7)
	var secret Secret
	if err != nil || json.Unmarshal(plain, &secret) != nil || secret.Token != "fixture-migrated-token" {
		t.Fatal("credential did not reencrypt")
	}
	if _, err := NewService(s).resolve(t.Context(), "modis", nil); err != nil {
		t.Fatal("migrated route not usable", err)
	}
	if _, err := s.ImportLegacy(t.Context(), database, key); err == nil {
		t.Fatal("nonempty target overwritten")
	}
	for _, path := range []string{database, key} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("source removed")
		}
	}
}

func TestLegacyMigrationRollsBackEntireImport(t *testing.T) {
	s := testStore(t)
	database, key := legacyFixture(t, true)
	if _, err := s.ImportLegacy(t.Context(), database, key); err == nil {
		t.Fatal("corrupt payload accepted")
	}
	pools, err := s.Pools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(pools.Credentials)+len(pools.Proxies)+len(pools.Accounts) != 0 {
		t.Fatal("partial migration committed")
	}
}
