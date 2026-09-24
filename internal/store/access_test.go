package store

import (
	"context"
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

	credential, err := state.UpsertCredential(ctx, 0, "LAADS main", "laads", []byte("ciphertext"), "••••1234", true, 20)
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
