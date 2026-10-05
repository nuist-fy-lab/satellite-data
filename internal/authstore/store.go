package authstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	passwordIterations = 600_000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	secretBytes        = 32
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

type Store struct {
	db *sql.DB
}

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

type APIKey struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	RevokedAt  *string `json:"revoked_at"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", path+separator+"_busy_timeout=30000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=30000`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_salt BLOB NOT NULL,
			password_hash BLOB NOT NULL,
			password_iterations INTEGER NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin','user')),
			disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS satellite_api_keys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			prefix TEXT NOT NULL,
			secret_hash BLOB NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT,
			revoked_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS satellite_api_keys_user_idx ON satellite_api_keys(user_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS satellite_web_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			secret_hash BLOB NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS satellite_web_sessions_user_idx ON satellite_web_sessions(user_id, expires_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize auth store: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateUser(ctx context.Context, username, password, role string) (User, error) {
	username = strings.TrimSpace(username)
	role = strings.ToLower(strings.TrimSpace(role))
	if !usernamePattern.MatchString(username) {
		return User{}, errors.New("username must match [A-Za-z0-9._@-] and be at most 64 characters")
	}
	if len(password) < 10 || len(password) > 512 {
		return User{}, errors.New("password must be between 10 and 512 characters")
	}
	if role != "admin" && role != "user" {
		return User{}, errors.New("role must be admin or user")
	}
	salt, err := randomBytes(passwordSaltBytes)
	if err != nil {
		return User{}, err
	}
	digest := pbkdf2SHA256([]byte(password), salt, passwordIterations, passwordKeyBytes)
	id, err := randomHex(16)
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users(id,username,password_salt,password_hash,password_iterations,role,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		id, username, salt, digest, passwordIterations, role, now, now,
	)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return User{ID: id, Username: username, Role: role, CreatedAt: now}, nil
}

func (s *Store) SetPassword(ctx context.Context, username, password string) error {
	if len(password) < 10 || len(password) > 512 {
		return errors.New("password must be between 10 and 512 characters")
	}
	salt, err := randomBytes(passwordSaltBytes)
	if err != nil {
		return err
	}
	digest := pbkdf2SHA256([]byte(password), salt, passwordIterations, passwordKeyBytes)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_salt=?,password_hash=?,password_iterations=?,updated_at=? WHERE username=? COLLATE NOCASE`,
		salt, digest, passwordIterations, now, strings.TrimSpace(username),
	)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM satellite_web_sessions WHERE user_id=(SELECT id FROM users WHERE username=? COLLATE NOCASE)`,
		strings.TrimSpace(username),
	)
	return nil
}

func (s *Store) SetDisabled(ctx context.Context, username string, disabled bool) error {
	value := 0
	if disabled {
		value = 1
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET disabled=?,updated_at=? WHERE username=? COLLATE NOCASE`,
		value, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(username),
	)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	if disabled {
		_, _ = s.db.ExecContext(ctx,
			`DELETE FROM satellite_web_sessions WHERE user_id=(SELECT id FROM users WHERE username=? COLLATE NOCASE)`,
			strings.TrimSpace(username),
		)
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,username,role,disabled,created_at FROM users ORDER BY username COLLATE NOCASE`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var user User
		var disabled int
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &disabled, &user.CreatedAt); err != nil {
			return nil, err
		}
		user.Disabled = disabled != 0
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) VerifyPassword(ctx context.Context, username, password string) (User, error) {
	var user User
	var disabled int
	var salt, expected []byte
	var iterations int
	err := s.db.QueryRowContext(ctx,
		`SELECT id,username,role,disabled,created_at,password_salt,password_hash,password_iterations
		 FROM users WHERE username=? COLLATE NOCASE`, strings.TrimSpace(username),
	).Scan(&user.ID, &user.Username, &user.Role, &disabled, &user.CreatedAt, &salt, &expected, &iterations)
	if err != nil {
		return User{}, err
	}
	if disabled != 0 {
		return User{}, errors.New("user disabled")
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	if !hmac.Equal(actual, expected) {
		return User{}, errors.New("invalid credentials")
	}
	return user, nil
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	id, err := randomHex(12)
	if err != nil {
		return "", err
	}
	secret, err := randomBytes(secretBytes)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO satellite_web_sessions(id,user_id,secret_hash,created_at,last_used_at,expires_at) VALUES(?,?,?,?,?,?)`,
		id, userID, hashSecret(secret), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", err
	}
	return "satellitesess_v1_" + id + "_" + base64.RawURLEncoding.EncodeToString(secret), nil
}

func (s *Store) VerifySession(ctx context.Context, token string) (User, error) {
	id, secret, err := parseToken(token, "satellitesess_v1")
	if err != nil {
		return User{}, err
	}
	var user User
	var disabled int
	var expected []byte
	var expiresText string
	err = s.db.QueryRowContext(ctx,
		`SELECT u.id,u.username,u.role,u.disabled,u.created_at,s.secret_hash,s.expires_at
		 FROM satellite_web_sessions s JOIN users u ON u.id=s.user_id WHERE s.id=?`, id,
	).Scan(&user.ID, &user.Username, &user.Role, &disabled, &user.CreatedAt, &expected, &expiresText)
	if err != nil {
		return User{}, err
	}
	if disabled != 0 || !hmac.Equal(hashSecret(secret), expected) {
		return User{}, errors.New("invalid session")
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresText)
	if err != nil || time.Now().UTC().After(expires) {
		return User{}, errors.New("session expired")
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE satellite_web_sessions SET last_used_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return user, nil
}

func (s *Store) RevokeSession(ctx context.Context, token string) error {
	id, _, err := parseToken(token, "satellitesess_v1")
	if err != nil {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM satellite_web_sessions WHERE id=?`, id)
	return err
}

func (s *Store) CreateAPIKey(ctx context.Context, userID, name string) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return APIKey{}, "", errors.New("API key name must be 1 to 80 characters")
	}
	id, err := randomHex(10)
	if err != nil {
		return APIKey{}, "", err
	}
	secret, err := randomBytes(secretBytes)
	if err != nil {
		return APIKey{}, "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	prefix := "satellite_v1_" + id
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO satellite_api_keys(id,user_id,name,prefix,secret_hash,created_at) VALUES(?,?,?,?,?,?)`,
		id, userID, name, prefix, hashSecret(secret), now,
	)
	if err != nil {
		return APIKey{}, "", err
	}
	key := APIKey{ID: id, Name: name, Prefix: prefix, CreatedAt: now}
	return key, "satellite_v1_" + id + "_" + encoded, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,prefix,created_at,last_used_at,revoked_at FROM satellite_api_keys WHERE user_id=? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]APIKey, 0)
	for rows.Next() {
		var item APIKey
		var lastUsed, revoked sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &item.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			item.LastUsedAt = &lastUsed.String
		}
		if revoked.Valid {
			item.RevokedAt = &revoked.String
		}
		keys = append(keys, item)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, userID, keyID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE satellite_api_keys SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), keyID, userID,
	)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, token string) (User, error) {
	id, secret, err := parseToken(token, "satellite_v1")
	if err != nil {
		return User{}, err
	}
	var user User
	var disabled int
	var expected []byte
	var revoked sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT u.id,u.username,u.role,u.disabled,u.created_at,k.secret_hash,k.revoked_at
		 FROM satellite_api_keys k JOIN users u ON u.id=k.user_id WHERE k.id=?`, id,
	).Scan(&user.ID, &user.Username, &user.Role, &disabled, &user.CreatedAt, &expected, &revoked)
	if err != nil {
		return User{}, err
	}
	if disabled != 0 || revoked.Valid || !hmac.Equal(hashSecret(secret), expected) {
		return User{}, errors.New("invalid API key")
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE satellite_api_keys SET last_used_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return user, nil
}

func randomBytes(n int) ([]byte, error) {
	value := make([]byte, n)
	_, err := rand.Read(value)
	return value, err
}

func randomHex(n int) (string, error) {
	value, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func hashSecret(secret []byte) []byte {
	digest := sha256.Sum256(secret)
	return digest[:]
}

func parseToken(token, prefix string) (string, []byte, error) {
	parts := strings.SplitN(token, "_", 4)
	// Prefix, version, public id, then the URL-safe secret (which may contain '_').
	if len(parts) != 4 || parts[0]+"_"+parts[1] != prefix {
		return "", nil, errors.New("invalid token format")
	}
	id := parts[2]
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != secretBytes {
		return "", nil, errors.New("invalid token secret")
	}
	return id, secret, nil
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	const hashLength = 32
	blocks := (keyLength + hashLength - 1) / hashLength
	result := make([]byte, 0, blocks*hashLength)
	for block := 1; block <= blocks; block++ {
		counter := make([]byte, 4)
		binary.BigEndian.PutUint32(counter, uint32(block))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(counter)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLength]
}
