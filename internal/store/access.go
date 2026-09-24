package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
)

const accessSchema = `
CREATE TABLE IF NOT EXISTS credentials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind TEXT NOT NULL,
  encrypted_secret BLOB NOT NULL,
  secret_hint TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  priority INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'ready',
  last_error TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT '',
  last_success_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS credentials_kind_idx ON credentials(kind,enabled,priority DESC,id);

CREATE TABLE IF NOT EXISTS proxies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias TEXT NOT NULL UNIQUE COLLATE NOCASE,
  encrypted_url BLOB NOT NULL,
  scheme TEXT NOT NULL,
  endpoint_hint TEXT NOT NULL DEFAULT '',
  providers_json TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  priority INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'ready',
  last_error TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT '',
  last_success_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS proxies_ready_idx ON proxies(enabled,state,priority DESC,id);

CREATE TABLE IF NOT EXISTS accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias TEXT NOT NULL UNIQUE COLLATE NOCASE,
  source_id TEXT NOT NULL,
  credential_id INTEGER REFERENCES credentials(id) ON DELETE SET NULL,
  proxy_mode TEXT NOT NULL DEFAULT 'direct' CHECK(proxy_mode IN ('direct','auto','fixed')),
  proxy_id INTEGER REFERENCES proxies(id) ON DELETE SET NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  priority INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'ready',
  last_error TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT '',
  last_success_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS accounts_source_idx ON accounts(source_id,enabled,state,priority DESC,id);
`

func (s *Store) UpsertCredential(ctx context.Context, id int64, alias, kind string, encrypted []byte, hint string, enabled bool, priority int) (model.Credential, error) {
	alias = strings.TrimSpace(alias)
	kind = strings.TrimSpace(kind)
	if alias == "" || len(alias) > 64 || kind == "" {
		return model.Credential{}, errors.New("invalid credential alias or kind")
	}
	if priority < -1000 || priority > 1000 {
		return model.Credential{}, errors.New("priority out of range")
	}
	now := stamp(time.Now())
	if id <= 0 {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO credentials(alias,kind,encrypted_secret,secret_hint,enabled,priority,state,created_at,updated_at)
			VALUES(?,?,?,?,?,?,'ready',?,?)`,
			alias, kind, encrypted, hint, boolInt(enabled), priority, now, now)
		if err != nil {
			return model.Credential{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return model.Credential{}, err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `
			UPDATE credentials
			SET alias=?,kind=?,encrypted_secret=?,secret_hint=?,enabled=?,priority=?,state='ready',last_error='',updated_at=?
			WHERE id=?`,
			alias, kind, encrypted, hint, boolInt(enabled), priority, now, id)
		if err != nil {
			return model.Credential{}, err
		}
	}
	return s.Credential(ctx, id)
}

func (s *Store) Credentials(ctx context.Context) ([]model.Credential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,alias,kind,enabled,priority,secret_hint,state,last_error,last_used_at,last_success_at,updated_at
		FROM credentials ORDER BY priority DESC,alias COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Credential
	for rows.Next() {
		item, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Credential(ctx context.Context, id int64) (model.Credential, error) {
	return scanCredential(s.db.QueryRowContext(ctx, `
		SELECT id,alias,kind,enabled,priority,secret_hint,state,last_error,last_used_at,last_success_at,updated_at
		FROM credentials WHERE id=?`, id))
}

func (s *Store) CredentialSecret(ctx context.Context, id int64) ([]byte, string, error) {
	var encrypted []byte
	var kind string
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_secret,kind FROM credentials WHERE id=?`, id).Scan(&encrypted, &kind)
	return encrypted, kind, err
}

func (s *Store) DeleteCredential(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpsertProxy(ctx context.Context, id int64, alias string, encrypted []byte, scheme, hint string, providers []string, enabled bool, priority int) (model.Proxy, error) {
	alias = strings.TrimSpace(alias)
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if alias == "" || len(alias) > 64 || (scheme != "http" && scheme != "https" && scheme != "socks5") {
		return model.Proxy{}, errors.New("invalid proxy")
	}
	if priority < -1000 || priority > 1000 {
		return model.Proxy{}, errors.New("priority out of range")
	}
	encoded, err := json.Marshal(normalizeProviders(providers))
	if err != nil {
		return model.Proxy{}, err
	}
	now := stamp(time.Now())
	if id <= 0 {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO proxies(alias,encrypted_url,scheme,endpoint_hint,providers_json,enabled,priority,state,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,'ready',?,?)`,
			alias, encrypted, scheme, hint, string(encoded), boolInt(enabled), priority, now, now)
		if err != nil {
			return model.Proxy{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return model.Proxy{}, err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `
			UPDATE proxies
			SET alias=?,encrypted_url=?,scheme=?,endpoint_hint=?,providers_json=?,enabled=?,priority=?,state='ready',last_error='',updated_at=?
			WHERE id=?`,
			alias, encrypted, scheme, hint, string(encoded), boolInt(enabled), priority, now, id)
		if err != nil {
			return model.Proxy{}, err
		}
	}
	return s.Proxy(ctx, id)
}

func (s *Store) Proxies(ctx context.Context) ([]model.Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,alias,scheme,endpoint_hint,providers_json,enabled,priority,state,last_error,last_used_at,last_success_at,updated_at
		FROM proxies ORDER BY priority DESC,alias COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Proxy
	for rows.Next() {
		item, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Proxy(ctx context.Context, id int64) (model.Proxy, error) {
	return scanProxy(s.db.QueryRowContext(ctx, `
		SELECT id,alias,scheme,endpoint_hint,providers_json,enabled,priority,state,last_error,last_used_at,last_success_at,updated_at
		FROM proxies WHERE id=?`, id))
}

func (s *Store) ProxySecret(ctx context.Context, id int64) ([]byte, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_url FROM proxies WHERE id=?`, id).Scan(&encrypted)
	return encrypted, err
}

func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpsertAccount(ctx context.Context, id int64, alias, sourceID string, credentialID, proxyID *int64, proxyMode string, enabled bool, priority int) (model.Account, error) {
	alias = strings.TrimSpace(alias)
	sourceID = strings.TrimSpace(sourceID)
	proxyMode = strings.TrimSpace(proxyMode)
	if alias == "" || len(alias) > 64 || sourceID == "" || (proxyMode != "direct" && proxyMode != "auto" && proxyMode != "fixed") {
		return model.Account{}, errors.New("invalid account")
	}
	if proxyMode == "fixed" && proxyID == nil {
		return model.Account{}, errors.New("fixed proxy mode requires proxy_id")
	}
	if priority < -1000 || priority > 1000 {
		return model.Account{}, errors.New("priority out of range")
	}
	now := stamp(time.Now())
	if id <= 0 {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO accounts(alias,source_id,credential_id,proxy_mode,proxy_id,enabled,priority,state,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,'ready',?,?)`,
			alias, sourceID, nullableID(credentialID), proxyMode, nullableID(proxyID), boolInt(enabled), priority, now, now)
		if err != nil {
			return model.Account{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return model.Account{}, err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `
			UPDATE accounts
			SET alias=?,source_id=?,credential_id=?,proxy_mode=?,proxy_id=?,enabled=?,priority=?,state='ready',last_error='',updated_at=?
			WHERE id=?`,
			alias, sourceID, nullableID(credentialID), proxyMode, nullableID(proxyID), boolInt(enabled), priority, now, id)
		if err != nil {
			return model.Account{}, err
		}
	}
	return s.Account(ctx, id)
}

func (s *Store) Accounts(ctx context.Context) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, accountSelect+` ORDER BY a.priority DESC,a.alias COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Account
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Account(ctx context.Context, id int64) (model.Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, accountSelect+` WHERE a.id=?`, id))
}

func (s *Store) AccountsForSource(ctx context.Context, sourceID string) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, accountSelect+`
		WHERE a.source_id=? AND a.enabled=1 AND a.state!='auth_required'
		ORDER BY a.priority DESC,
		CASE WHEN a.last_used_at='' THEN 0 ELSE 1 END,
		a.last_used_at ASC,a.id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Account
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) HasAccountsForSource(ctx context.Context, sourceID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE source_id=?`, sourceID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MarkAccountUsed(ctx context.Context, accountID int64, proxyID *int64) error {
	now := stamp(time.Now())
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET last_used_at=?,updated_at=? WHERE id=?`, now, now, accountID); err != nil {
		return err
	}
	if proxyID != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE proxies SET last_used_at=?,updated_at=? WHERE id=?`, now, now, *proxyID)
	}
	return nil
}

func (s *Store) MarkRouteSuccess(ctx context.Context, accountID int64, credentialID, proxyID *int64) {
	now := stamp(time.Now())
	_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET state='ready',last_error='',last_success_at=?,updated_at=? WHERE id=?`, now, now, accountID)
	if credentialID != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE credentials SET state='ready',last_error='',last_success_at=?,updated_at=? WHERE id=?`, now, now, *credentialID)
	}
	if proxyID != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE proxies SET state='ready',last_error='',last_success_at=?,updated_at=? WHERE id=?`, now, now, *proxyID)
	}
}

func (s *Store) MarkAccountAuthFailure(ctx context.Context, accountID int64, credentialID *int64, detail string) {
	now := stamp(time.Now())
	_, _ = s.db.ExecContext(ctx, `UPDATE accounts SET state='auth_required',last_error=?,updated_at=? WHERE id=?`, detail, now, accountID)
	if credentialID != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE credentials SET state='auth_required',last_error=?,updated_at=? WHERE id=?`, detail, now, *credentialID)
	}
}

func (s *Store) MarkProxyFailure(ctx context.Context, proxyID int64, detail string) {
	now := stamp(time.Now())
	_, _ = s.db.ExecContext(ctx, `UPDATE proxies SET state='degraded',last_error=?,updated_at=? WHERE id=?`, detail, now, proxyID)
}

func (s *Store) EligibleProxies(ctx context.Context, sourceID string) ([]model.Proxy, error) {
	items, err := s.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Proxy, 0, len(items))
	for _, item := range items {
		if !item.Enabled || item.State == "disabled" {
			continue
		}
		if proxyAllows(item.Providers, sourceID) {
			out = append(out, item)
		}
	}
	return out, nil
}

const accountSelect = `
SELECT a.id,a.alias,a.source_id,a.credential_id,COALESCE(c.alias,''),a.proxy_mode,a.proxy_id,COALESCE(p.alias,''),
       a.enabled,a.priority,a.state,a.last_error,a.last_used_at,a.last_success_at,a.updated_at
FROM accounts a
LEFT JOIN credentials c ON c.id=a.credential_id
LEFT JOIN proxies p ON p.id=a.proxy_id`

type accessScanner interface{ Scan(...any) error }

func scanCredential(row accessScanner) (model.Credential, error) {
	var item model.Credential
	var enabled int
	err := row.Scan(&item.ID, &item.Alias, &item.Kind, &enabled, &item.Priority, &item.SecretHint, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &item.UpdatedAt)
	item.Enabled = enabled != 0
	return item, err
}

func scanProxy(row accessScanner) (model.Proxy, error) {
	var item model.Proxy
	var enabled int
	var providers string
	err := row.Scan(&item.ID, &item.Alias, &item.Scheme, &item.EndpointHint, &providers, &enabled, &item.Priority, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &item.UpdatedAt)
	item.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(providers), &item.Providers)
	return item, err
}

func scanAccount(row accessScanner) (model.Account, error) {
	var item model.Account
	var credentialID, proxyID sql.NullInt64
	var enabled int
	err := row.Scan(&item.ID, &item.Alias, &item.SourceID, &credentialID, &item.CredentialAlias, &item.ProxyMode, &proxyID, &item.ProxyAlias,
		&enabled, &item.Priority, &item.State, &item.LastError, &item.LastUsedAt, &item.LastSuccessAt, &item.UpdatedAt)
	if credentialID.Valid {
		value := credentialID.Int64
		item.CredentialID = &value
	}
	if proxyID.Valid {
		value := proxyID.Int64
		item.ProxyID = &value
	}
	item.Enabled = enabled != 0
	return item, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func normalizeProviders(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func proxyAllows(values []string, sourceID string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == "*" || value == sourceID {
			return true
		}
	}
	return false
}
