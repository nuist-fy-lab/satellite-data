package central

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS credentials(id INTEGER PRIMARY KEY,alias TEXT NOT NULL UNIQUE COLLATE NOCASE,metadata TEXT NOT NULL,encrypted_secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS proxies(id INTEGER PRIMARY KEY,alias TEXT NOT NULL UNIQUE COLLATE NOCASE,metadata TEXT NOT NULL,encrypted_secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS accounts(id INTEGER PRIMARY KEY,alias TEXT NOT NULL UNIQUE COLLATE NOCASE,credential_id INTEGER REFERENCES credentials(id) ON DELETE SET NULL,proxy_id INTEGER REFERENCES proxies(id) ON DELETE SET NULL,metadata TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS cds_jobs(job_id TEXT PRIMARY KEY,account_id INTEGER REFERENCES accounts(id) ON DELETE SET NULL,credential_id INTEGER REFERENCES credentials(id) ON DELETE SET NULL,token_hash TEXT NOT NULL);
`

type Store struct {
	db    *sql.DB
	vault *Vault
}
type CredentialSpec struct {
	ID       int64  `json:"id,omitempty"`
	Alias    string `json:"alias"`
	Kind     string `json:"kind"`
	Secret   Secret `json:"secret"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
}
type ProxySpec struct {
	ID        int64    `json:"id,omitempty"`
	Alias     string   `json:"alias"`
	URL       string   `json:"url"`
	Providers []string `json:"providers"`
	Enabled   bool     `json:"enabled"`
	Priority  int      `json:"priority"`
}
type AccountSpec struct {
	ID           int64  `json:"id,omitempty"`
	Alias        string `json:"alias"`
	SourceID     string `json:"source_id"`
	CredentialID *int64 `json:"credential_id,omitempty"`
	ProxyID      *int64 `json:"proxy_id,omitempty"`
	ProxyMode    string `json:"proxy_mode"`
	Enabled      bool   `json:"enabled"`
	Priority     int    `json:"priority"`
}

func OpenStore(filename string, vault *Vault) (*Store, error) {
	if vault == nil {
		return nil, errors.New("central vault is required")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=30000`); err == nil {
		_, err = db.Exec(schema)
	}
	if err == nil {
		err = os.Chmod(filename, 0600)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, vault: vault}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func validSpec(id int64, alias string, priority int) bool {
	return id >= 0 && strings.TrimSpace(alias) != "" && len(alias) <= 64 && priority >= -1000 && priority <= 1000
}

func (s *Store) readMetadata(ctx context.Context, table string, id int64, target any) error {
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT metadata FROM "+table+" WHERE id=?", id).Scan(&data); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func (s *Store) readSecret(ctx context.Context, table string, id int64) ([]byte, error) {
	var encrypted []byte
	if err := s.db.QueryRowContext(ctx, "SELECT encrypted_secret FROM "+table+" WHERE id=?", id).Scan(&encrypted); err != nil {
		return nil, err
	}
	return s.vault.Open(encrypted)
}
func (s *Store) save(ctx context.Context, table string, id int64, alias string, metadata any, encrypted []byte) (int64, error) {
	data, err := json.Marshal(metadata)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		result, err := s.db.ExecContext(ctx, "INSERT INTO "+table+"(alias,metadata,encrypted_secret) VALUES(?,?,?)", alias, data, encrypted)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	result, err := s.db.ExecContext(ctx, "UPDATE "+table+" SET alias=?,metadata=?,encrypted_secret=COALESCE(?,encrypted_secret) WHERE id=?", alias, data, encrypted, id)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return 0, sql.ErrNoRows
	}
	return id, nil
}

func (s *Store) UpsertCredential(ctx context.Context, spec CredentialSpec) (downloader.Credential, error) {
	if !validSpec(spec.ID, spec.Alias, spec.Priority) {
		return downloader.Credential{}, errors.New("invalid central credential")
	}
	var encrypted []byte
	var existing downloader.Credential
	if spec.ID > 0 {
		if err := s.readMetadata(ctx, "credentials", spec.ID, &existing); err != nil {
			return existing, err
		}
	}
	if spec.ID == 0 || spec.Secret != (Secret{}) {
		if err := ValidateSecret(spec.Kind, spec.Secret); err != nil {
			return downloader.Credential{}, err
		}
		plain, err := json.Marshal(spec.Secret)
		if err != nil {
			return downloader.Credential{}, err
		}
		encrypted, err = s.vault.Seal(plain)
		if err != nil {
			return downloader.Credential{}, errors.New("central vault unavailable")
		}
	} else if existing.Kind != spec.Kind {
		return existing, errors.New("changing credential kind requires a new secret")
	}
	item := downloader.Credential{Storage: "central", Alias: strings.TrimSpace(spec.Alias), Kind: spec.Kind, Enabled: spec.Enabled, Priority: spec.Priority, State: "ready", SecretHint: "configured", UpdatedAt: stamp()}
	if encrypted != nil {
		expiry, err := InspectCredentialExpiry(spec.Kind, encrypted, s.vault)
		if err != nil {
			return item, errors.New("credential expiry could not be inspected")
		}
		item.ExpiryStatus, item.ExpirySource, item.ExpiresAt, item.ExpiryCheckedAt = expiry.Status, expiry.Source, expiry.ExpiresAt, expiry.CheckedAt
	} else {
		item.ExpiryStatus, item.ExpirySource, item.ExpiresAt, item.ExpiryCheckedAt = existing.ExpiryStatus, existing.ExpirySource, existing.ExpiresAt, existing.ExpiryCheckedAt
	}
	if !spec.Enabled {
		item.State = "disabled"
	}
	id, err := s.save(ctx, "credentials", spec.ID, item.Alias, item, encrypted)
	item.ID = id
	return item, err
}

func (s *Store) UpsertProxy(ctx context.Context, spec ProxySpec) (downloader.Proxy, error) {
	if !validSpec(spec.ID, spec.Alias, spec.Priority) {
		return downloader.Proxy{}, errors.New("invalid central proxy")
	}
	var encrypted []byte
	var item downloader.Proxy
	if spec.ID > 0 {
		if err := s.readMetadata(ctx, "proxies", spec.ID, &item); err != nil {
			return item, err
		}
	}
	if spec.URL != "" || spec.ID == 0 {
		u, err := url.Parse(strings.TrimSpace(spec.URL))
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return item, errors.New("proxy must be an HTTP, HTTPS or SOCKS5 proxy URL")
		}
		encrypted, err = s.vault.Seal([]byte(u.String()))
		if err != nil {
			return item, errors.New("central vault unavailable")
		}
		item.Scheme, item.EndpointHint = u.Scheme, u.Host
	}
	for _, source := range spec.Providers {
		if source != "*" {
			if _, err := ProviderKind(source); err != nil {
				return item, err
			}
		}
	}
	item.Storage, item.Alias, item.Enabled, item.Priority, item.Providers, item.UpdatedAt = "central", strings.TrimSpace(spec.Alias), spec.Enabled, spec.Priority, spec.Providers, stamp()
	item.State = "ready"
	if !item.Enabled {
		item.State = "disabled"
	}
	id, err := s.save(ctx, "proxies", spec.ID, item.Alias, item, encrypted)
	item.ID = id
	return item, err
}

func (s *Store) UpsertAccount(ctx context.Context, spec AccountSpec) (downloader.Account, error) {
	if !validSpec(spec.ID, spec.Alias, spec.Priority) {
		return downloader.Account{}, errors.New("invalid central account")
	}
	kind, err := ProviderKind(spec.SourceID)
	if err != nil {
		return downloader.Account{}, err
	}
	var credential downloader.Credential
	if kind != "" {
		if spec.CredentialID == nil {
			return downloader.Account{}, errors.New("provider requires a central credential")
		}
		if err := s.readMetadata(ctx, "credentials", *spec.CredentialID, &credential); err != nil || credential.Kind != kind {
			return downloader.Account{}, errors.New("credential does not match provider")
		}
	} else if spec.CredentialID != nil {
		return downloader.Account{}, errors.New("anonymous provider must not bind a credential")
	}
	var proxy downloader.Proxy
	switch spec.ProxyMode {
	case "direct", "auto":
		spec.ProxyID = nil
	case "fixed":
		if spec.ProxyID == nil {
			return downloader.Account{}, errors.New("fixed proxy is required")
		}
		if err := s.readMetadata(ctx, "proxies", *spec.ProxyID, &proxy); err != nil {
			return downloader.Account{}, errors.New("central proxy not found")
		}
	default:
		return downloader.Account{}, errors.New("invalid proxy mode")
	}
	item := downloader.Account{Storage: "central", Alias: strings.TrimSpace(spec.Alias), SourceID: spec.SourceID, CredentialID: spec.CredentialID, CredentialAlias: credential.Alias, ProxyID: spec.ProxyID, ProxyAlias: proxy.Alias, ProxyMode: spec.ProxyMode, Enabled: spec.Enabled, Priority: spec.Priority, State: "ready", UpdatedAt: stamp()}
	if !item.Enabled {
		item.State = "disabled"
	}
	data, err := json.Marshal(item)
	if err != nil {
		return item, err
	}
	if spec.ID == 0 {
		result, err := s.db.ExecContext(ctx, `INSERT INTO accounts(alias,credential_id,proxy_id,metadata) VALUES(?,?,?,?)`, item.Alias, item.CredentialID, item.ProxyID, data)
		if err != nil {
			return item, err
		}
		item.ID, err = result.LastInsertId()
		return item, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE accounts SET alias=?,credential_id=?,proxy_id=?,metadata=? WHERE id=?`, item.Alias, item.CredentialID, item.ProxyID, data, spec.ID)
	if err != nil {
		return item, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return item, sql.ErrNoRows
	}
	item.ID = spec.ID
	return item, nil
}

func (s *Store) records(ctx context.Context, table string, decode func(int64, []byte) error) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id,metadata FROM "+table+" ORDER BY alias COLLATE NOCASE")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		if err := decode(id, data); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *Store) Pools(ctx context.Context) (downloader.AccessPools, error) {
	pools := downloader.AccessPools{Credentials: []downloader.Credential{}, Proxies: []downloader.Proxy{}, Accounts: []downloader.Account{}}
	if err := s.records(ctx, "credentials", func(id int64, data []byte) error {
		var item downloader.Credential
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		item.ID = id
		pools.Credentials = append(pools.Credentials, item)
		return nil
	}); err != nil {
		return pools, err
	}
	if err := s.records(ctx, "proxies", func(id int64, data []byte) error {
		var item downloader.Proxy
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		item.ID = id
		pools.Proxies = append(pools.Proxies, item)
		return nil
	}); err != nil {
		return pools, err
	}
	credentials := make(map[int64]string, len(pools.Credentials))
	for _, item := range pools.Credentials {
		credentials[item.ID] = item.Alias
	}
	proxies := make(map[int64]string, len(pools.Proxies))
	for _, item := range pools.Proxies {
		proxies[item.ID] = item.Alias
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,credential_id,proxy_id,metadata FROM accounts ORDER BY alias COLLATE NOCASE`)
	if err != nil {
		return pools, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var credentialID, proxyID sql.NullInt64
		var data []byte
		if err := rows.Scan(&id, &credentialID, &proxyID, &data); err != nil {
			return pools, err
		}
		var item downloader.Account
		if err := json.Unmarshal(data, &item); err != nil {
			return pools, err
		}
		item.ID = id
		item.CredentialID = nil
		if credentialID.Valid {
			value := credentialID.Int64
			item.CredentialID = &value
		}
		item.ProxyID = nil
		if proxyID.Valid {
			value := proxyID.Int64
			item.ProxyID = &value
		}
		item.CredentialAlias = ""
		item.ProxyAlias = ""
		if item.CredentialID != nil {
			item.CredentialAlias = credentials[*item.CredentialID]
		}
		if item.ProxyID != nil {
			item.ProxyAlias = proxies[*item.ProxyID]
		}
		pools.Accounts = append(pools.Accounts, item)
	}
	if err := rows.Err(); err != nil {
		return pools, err
	}
	sort.SliceStable(pools.Accounts, func(i, j int) bool {
		a, b := pools.Accounts[i], pools.Accounts[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.LastUsedAt < b.LastUsedAt
	})
	return pools, nil
}

func (s *Store) Delete(ctx context.Context, kind string, id int64) error {
	if id <= 0 || (kind != "credentials" && kind != "proxies" && kind != "accounts") {
		return errors.New("invalid central pool item")
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM "+kind+" WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

type Binding struct {
	AccountID, CredentialID int64
	TokenHash               string
}

func (s *Store) SaveBinding(ctx context.Context, jobID string, binding Binding) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cds_jobs(job_id,account_id,credential_id,token_hash) VALUES(?,?,?,?) ON CONFLICT(job_id) DO NOTHING`, jobID, binding.AccountID, binding.CredentialID, binding.TokenHash)
	if err != nil {
		return err
	}
	actual, err := s.Binding(ctx, jobID)
	if err != nil {
		return err
	}
	if actual != binding {
		return errors.New("CDS job already belongs to another route")
	}
	return nil
}
func (s *Store) Binding(ctx context.Context, jobID string) (Binding, error) {
	var binding Binding
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(account_id,0),COALESCE(credential_id,0),token_hash FROM cds_jobs WHERE job_id=?`, jobID).Scan(&binding.AccountID, &binding.CredentialID, &binding.TokenHash)
	return binding, err
}
