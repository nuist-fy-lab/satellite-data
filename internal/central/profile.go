package central

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
)

// Profile is the single write contract for a provider route and its secret.
// Existing shared credentials stay encrypted and referenced by ID.
type ProfileSpec struct {
	AccountSpec
	Secret Secret `json:"secret"`
}

func (s *Store) UpsertProfile(ctx context.Context, spec ProfileSpec) (downloader.Account, error) {
	if !validSpec(spec.ID, spec.Alias, spec.Priority) {
		return downloader.Account{}, errors.New("invalid access profile")
	}
	kind, err := ProviderKind(spec.SourceID)
	if err != nil {
		return downloader.Account{}, err
	}
	hasSecret := spec.Secret != (Secret{})
	if kind == "" && (hasSecret || spec.CredentialID != nil) {
		return downloader.Account{}, errors.New("anonymous provider cannot use a credential")
	}
	if hasSecret && spec.CredentialID != nil {
		return downloader.Account{}, errors.New("choose a saved credential or enter a new secret, not both")
	}
	if hasSecret {
		if err := ValidateSecret(kind, spec.Secret); err != nil {
			return downloader.Account{}, err
		}
	}
	if spec.ProxyMode != "direct" && spec.ProxyMode != "auto" && spec.ProxyMode != "fixed" {
		return downloader.Account{}, errors.New("invalid proxy mode")
	}
	if spec.ProxyMode == "fixed" && spec.ProxyID == nil {
		return downloader.Account{}, errors.New("fixed proxy is required")
	}
	if spec.ProxyMode != "fixed" {
		spec.ProxyID = nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return downloader.Account{}, err
	}
	defer tx.Rollback()
	var account downloader.Account
	var oldCredential sql.NullInt64
	if spec.ID != 0 {
		var data []byte
		if err := tx.QueryRowContext(ctx, `SELECT credential_id,metadata FROM accounts WHERE id=?`, spec.ID).Scan(&oldCredential, &data); err != nil {
			return account, err
		}
		if err := json.Unmarshal(data, &account); err != nil {
			return account, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `INSERT INTO accounts(alias,metadata) VALUES(?, '{}')`, strings.TrimSpace(spec.Alias))
		if err != nil {
			return account, err
		}
		spec.ID, err = result.LastInsertId()
		if err != nil {
			return account, err
		}
	}
	var credential downloader.Credential
	credentialID := spec.CredentialID
	if kind != "" && !hasSecret && credentialID == nil && oldCredential.Valid {
		credentialID = &oldCredential.Int64
	}
	if kind != "" && !hasSecret && credentialID == nil {
		return account, errors.New("provider requires a credential or new secret")
	}
	if kind != "" && !hasSecret {
		var data []byte
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM credentials WHERE id=?`, *credentialID).Scan(&data); err != nil {
			return account, errors.New("saved credential not found")
		}
		if err := json.Unmarshal(data, &credential); err != nil {
			return account, err
		}
		if credential.Kind != kind {
			return account, errors.New("credential does not match provider")
		}
	}
	if hasSecret {
		plain, err := json.Marshal(spec.Secret)
		if err != nil {
			return account, err
		}
		encrypted, err := s.vault.Seal(plain)
		if err != nil {
			return account, errors.New("credential vault unavailable")
		}
		expiry, err := InspectCredentialExpiry(kind, encrypted, s.vault)
		if err != nil {
			return account, errors.New("credential expiry could not be inspected")
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return account, errors.New("credential identifier unavailable")
		}
		credential = downloader.Credential{
			Storage: "central", Alias: fmt.Sprintf("profile-%d-%s", spec.ID, hex.EncodeToString(nonce[:])), Kind: kind,
			Enabled: true, Priority: spec.Priority, State: "ready", SecretHint: "configured", UpdatedAt: stamp(),
			ExpiresAt: expiry.ExpiresAt, ExpiryStatus: expiry.Status, ExpirySource: expiry.Source, ExpiryCheckedAt: expiry.CheckedAt,
		}
		data, err := json.Marshal(credential)
		if err != nil {
			return account, err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO credentials(alias,metadata,encrypted_secret) VALUES(?,?,?)`, credential.Alias, data, encrypted)
		if err != nil {
			return account, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return account, err
		}
		credentialID = &id
	}
	var proxy downloader.Proxy
	if spec.ProxyID != nil {
		var data []byte
		if err := tx.QueryRowContext(ctx, `SELECT metadata FROM proxies WHERE id=?`, *spec.ProxyID).Scan(&data); err != nil {
			return account, errors.New("fixed proxy not found")
		}
		if err := json.Unmarshal(data, &proxy); err != nil {
			return account, err
		}
	}
	account = downloader.Account{
		Storage: "central", Alias: strings.TrimSpace(spec.Alias), SourceID: spec.SourceID,
		CredentialID: credentialID, CredentialAlias: credential.Alias, ProxyID: spec.ProxyID,
		ProxyAlias: proxy.Alias, ProxyMode: spec.ProxyMode, Enabled: spec.Enabled,
		Priority: spec.Priority, State: "ready", UpdatedAt: stamp(),
	}
	if !spec.Enabled {
		account.State = "disabled"
	}
	data, err := json.Marshal(account)
	if err != nil {
		return account, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET alias=?,credential_id=?,proxy_id=?,metadata=? WHERE id=?`, account.Alias, credentialID, spec.ProxyID, data, spec.ID); err != nil {
		return account, err
	}
	if err := tx.Commit(); err != nil {
		return account, err
	}
	account.ID = spec.ID
	return account, nil
}
