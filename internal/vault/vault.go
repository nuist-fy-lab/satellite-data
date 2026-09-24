package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keyBytes = 32

type Vault struct {
	aead cipher.AEAD
}

func Open(path string, create bool) (*Vault, error) {
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			key := make([]byte, keyBytes)
			if _, err := io.ReadFull(rand.Reader, key); err != nil {
				file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if _, err := file.Write(key); err != nil {
				file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("master key must be exactly %d bytes", keyBytes)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
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

func (v *Vault) Seal(plaintext []byte) ([]byte, error) {
	if v == nil {
		return nil, errors.New("vault is not configured")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := v.aead.Seal(nil, nonce, plaintext, []byte("satellite-data-downloader-v1"))
	result := append([]byte("v1:"), nonce...)
	return append(result, sealed...), nil
}

func (v *Vault) Open(payload []byte) ([]byte, error) {
	if v == nil {
		return nil, errors.New("vault is not configured")
	}
	if len(payload) < 3+v.aead.NonceSize() || string(payload[:3]) != "v1:" {
		return nil, errors.New("unsupported encrypted payload")
	}
	nonce := payload[3 : 3+v.aead.NonceSize()]
	ciphertext := payload[3+v.aead.NonceSize():]
	return v.aead.Open(nil, nonce, ciphertext, []byte("satellite-data-downloader-v1"))
}
