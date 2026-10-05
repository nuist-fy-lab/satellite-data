package central

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProfileBindsExistingCDSWithoutReadingOrRewritingSecret(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	credential, err := s.UpsertCredential(ctx, CredentialSpec{Alias: "CDS", Kind: "cds", Secret: Secret{Token: "fixture-cds-token"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := s.db.QueryRow(`SELECT encrypted_secret FROM credentials WHERE id=?`, credential.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	account, err := s.UpsertProfile(ctx, ProfileSpec{AccountSpec: AccountSpec{Alias: "ERA5", SourceID: "era5", CredentialID: &credential.ID, ProxyMode: "direct", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if account.CredentialID == nil || *account.CredentialID != credential.ID {
		t.Fatal("existing encrypted CDS credential was not bound")
	}
	var after []byte
	if err := s.db.QueryRow(`SELECT encrypted_secret FROM credentials WHERE id=?`, credential.ID).Scan(&after); err != nil || string(original) != string(after) {
		t.Fatal("binding rewrote the saved token")
	}
	if _, err := NewService(s).resolve(ctx, "era5", nil); err != nil {
		t.Fatal("ERA5 route remained unavailable:", err)
	}
}

func TestProfileCreationIsAtomicAndEditsDoNotOverwriteSharedCredential(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	spec := ProfileSpec{AccountSpec: AccountSpec{Alias: "first", SourceID: "modis", ProxyMode: "fixed", Enabled: true}, Secret: Secret{Token: "fixture-original"}}
	if _, err := s.UpsertProfile(ctx, spec); err == nil {
		t.Fatal("missing fixed proxy accepted")
	}
	pools, err := s.Pools(ctx)
	if err != nil || len(pools.Accounts) != 0 || len(pools.Credentials) != 0 {
		t.Fatal("failed save left an account or a secret behind", err)
	}
	spec.ProxyMode = "direct"
	first, err := s.UpsertProfile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.UpsertProfile(ctx, ProfileSpec{AccountSpec: AccountSpec{Alias: "second", SourceID: "viirs", CredentialID: first.CredentialID, ProxyMode: "direct", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpsertProfile(ctx, ProfileSpec{AccountSpec: AccountSpec{ID: first.ID, Alias: "first", SourceID: "modis", ProxyMode: "direct", Enabled: true}, Secret: Secret{Token: "fixture-new"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CredentialID == nil || second.CredentialID == nil || *updated.CredentialID == *second.CredentialID {
		t.Fatal("editing a profile overwrote a shared credential")
	}
	plain, err := s.readSecret(ctx, "credentials", *second.CredentialID)
	var saved Secret
	if err != nil || json.Unmarshal(plain, &saved) != nil || saved.Token != "fixture-original" {
		t.Fatal("another provider lost its original token")
	}
	if _, err := s.UpsertProfile(ctx, ProfileSpec{AccountSpec: AccountSpec{Alias: "invalid", SourceID: "era5", CredentialID: second.CredentialID, ProxyMode: "direct", Enabled: true}}); err == nil {
		t.Fatal("different provider accepted a LAADS credential")
	}
	pools, err = s.Pools(ctx)
	if err != nil || len(pools.Accounts) != 2 {
		t.Fatal("rejected profile left an account behind", err)
	}
}
