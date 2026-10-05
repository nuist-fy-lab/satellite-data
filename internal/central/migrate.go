package central

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
)

type ImportCounts struct{ Credentials, Proxies, Accounts int }

// ImportLegacy reads the stopped downloader's database and re-encrypts locally.
// The destination must be empty; all inserts commit together and preserve IDs.
func (s *Store) ImportLegacy(ctx context.Context, database, keyPath string) (counts ImportCounts, err error) {
	f, err := os.Open(keyPath)
	if err != nil {
		return counts, errors.New("legacy key is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return counts, errors.New("legacy key must be a private regular file")
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return counts, errors.New("invalid legacy key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return counts, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return counts, err
	}
	seal := func(payload []byte) ([]byte, error) {
		n := aead.NonceSize()
		if len(payload) < 3+n+aead.Overhead() || string(payload[:3]) != "v1:" {
			return nil, errors.New("invalid legacy encrypted payload")
		}
		plain, err := aead.Open(nil, payload[3:3+n], payload[3+n:], []byte("satellite-data-downloader-v1"))
		if err != nil {
			return nil, errors.New("legacy payload could not be decrypted")
		}
		return s.vault.Seal(plain)
	}
	path, err := filepath.Abs(database)
	if err != nil {
		return counts, err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	legacy, err := sql.Open("sqlite", u.String())
	if err != nil {
		return counts, errors.New("legacy database is unavailable")
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	source, err := legacy.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return counts, errors.New("legacy database is unavailable")
	}
	defer source.Rollback()
	target, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer target.Rollback()
	var existing int
	if err := target.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM credentials)+(SELECT count(*) FROM proxies)+(SELECT count(*) FROM accounts)+(SELECT count(*) FROM cds_jobs)`).Scan(&existing); err != nil {
		return counts, err
	}
	if existing != 0 {
		return counts, errors.New("central access store must be empty before migration")
	}
	insert := func(table string, id int64, alias string, item any, encrypted []byte) error {
		data, err := json.Marshal(item)
		if err != nil {
			return err
		}
		_, err = target.ExecContext(ctx, "INSERT INTO "+table+"(id,alias,metadata,encrypted_secret) VALUES(?,?,?,?)", id, alias, data, encrypted)
		return err
	}
	rows, err := source.QueryContext(ctx, `SELECT id,alias,kind,encrypted_secret,enabled,priority,state,last_error,last_used_at,last_success_at,created_at,updated_at FROM credentials`)
	if err != nil {
		return counts, errors.New("legacy credentials could not be read")
	}
	for rows.Next() {
		var item downloader.Credential
		var payload []byte
		var created string
		if err := rows.Scan(&item.ID, &item.Alias, &item.Kind, &payload, &item.Enabled, &item.Priority, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &created, &item.UpdatedAt); err != nil {
			rows.Close()
			return counts, err
		}
		encrypted, err := seal(payload)
		if err != nil {
			rows.Close()
			return counts, err
		}
		plain, err := s.vault.Open(encrypted)
		var secret Secret
		if err != nil || json.Unmarshal(plain, &secret) != nil {
			rows.Close()
			return counts, errors.New("invalid legacy credential")
		}
		if err := ValidateSecret(item.Kind, secret); err != nil {
			rows.Close()
			return counts, errors.New("legacy credential validation failed")
		}
		expiry, err := InspectCredentialExpiry(item.Kind, encrypted, s.vault)
		if err != nil {
			rows.Close()
			return counts, errors.New("legacy credential expiry could not be read")
		}
		item.Storage, item.SecretHint, item.LastError = "central", "configured", ""
		item.ExpiresAt, item.ExpiryStatus, item.ExpirySource, item.ExpiryCheckedAt = expiry.ExpiresAt, expiry.Status, expiry.Source, expiry.CheckedAt
		if err := insert("credentials", item.ID, item.Alias, item, encrypted); err != nil {
			rows.Close()
			return counts, err
		}
		counts.Credentials++
	}
	if err := finishRows(rows); err != nil {
		return counts, err
	}
	rows, err = source.QueryContext(ctx, `SELECT id,alias,encrypted_url,scheme,endpoint_hint,providers_json,enabled,priority,state,last_error,last_used_at,last_success_at,created_at,updated_at FROM proxies`)
	if err != nil {
		return counts, errors.New("legacy proxies could not be read")
	}
	for rows.Next() {
		var item downloader.Proxy
		var payload []byte
		var providers string
		var created string
		if err := rows.Scan(&item.ID, &item.Alias, &payload, &item.Scheme, &item.EndpointHint, &providers, &item.Enabled, &item.Priority, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &created, &item.UpdatedAt); err != nil {
			rows.Close()
			return counts, err
		}
		if json.Unmarshal([]byte(providers), &item.Providers) != nil {
			rows.Close()
			return counts, errors.New("invalid legacy proxy scope")
		}
		encrypted, err := seal(payload)
		if err != nil {
			rows.Close()
			return counts, err
		}
		plain, err := s.vault.Open(encrypted)
		u, parseErr := url.Parse(string(plain))
		if err != nil || parseErr != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			rows.Close()
			return counts, errors.New("invalid legacy proxy URL")
		}
		item.Storage, item.EndpointHint, item.LastError = "central", u.Host, ""
		if err := insert("proxies", item.ID, item.Alias, item, encrypted); err != nil {
			rows.Close()
			return counts, err
		}
		counts.Proxies++
	}
	if err := finishRows(rows); err != nil {
		return counts, err
	}
	rows, err = source.QueryContext(ctx, `SELECT id,alias,source_id,credential_id,proxy_id,proxy_mode,enabled,priority,state,last_error,last_used_at,last_success_at,created_at,updated_at FROM accounts`)
	if err != nil {
		return counts, errors.New("legacy accounts could not be read")
	}
	for rows.Next() {
		var item downloader.Account
		var created string
		if err := rows.Scan(&item.ID, &item.Alias, &item.SourceID, &item.CredentialID, &item.ProxyID, &item.ProxyMode, &item.Enabled, &item.Priority, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &created, &item.UpdatedAt); err != nil {
			rows.Close()
			return counts, err
		}
		if _, err := ProviderKind(item.SourceID); err != nil {
			rows.Close()
			return counts, errors.New("invalid legacy provider")
		}
		item.Storage, item.LastError = "central", ""
		data, err := json.Marshal(item)
		if err != nil {
			rows.Close()
			return counts, err
		}
		if _, err := target.ExecContext(ctx, `INSERT INTO accounts(id,alias,credential_id,proxy_id,metadata) VALUES(?,?,?,?,?)`, item.ID, item.Alias, item.CredentialID, item.ProxyID, data); err != nil {
			rows.Close()
			return counts, err
		}
		counts.Accounts++
	}
	if err := finishRows(rows); err != nil {
		return counts, err
	}
	if err := target.Commit(); err != nil {
		return counts, err
	}
	return counts, nil
}

func finishRows(rows *sql.Rows) error {
	err := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}
