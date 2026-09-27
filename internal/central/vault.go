package central

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Vault struct{ aead cipher.AEAD }

func OpenVault(filename string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		key := make([]byte, 32)
		_, writeErr := io.ReadFull(rand.Reader, key)
		if writeErr == nil {
			_, writeErr = f.Write(key)
		}
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(filename)
			return nil, errors.New("create central vault key failed")
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	f, err = os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("central vault key must be a private regular file")
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return nil, errors.New("central vault key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Seal(value []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, value, []byte("satellite-data-central-v1")), nil
}

func (v *Vault) Open(value []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(value) < n+v.aead.Overhead() {
		return nil, errors.New("invalid central encrypted payload")
	}
	return v.aead.Open(nil, value[:n], value[n:], []byte("satellite-data-central-v1"))
}
