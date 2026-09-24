package vault

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSealOpenUsesFreshNonce(t *testing.T) {
	v, err := Open(filepath.Join(t.TempDir(), "master.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("secret-value")
	first, err := v.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := v.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("AES-GCM payloads must use fresh nonces")
	}
	opened, err := v.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("opened=%q want=%q", opened, plain)
	}
}
