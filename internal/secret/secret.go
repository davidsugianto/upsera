// Package secret encrypts small secrets (notification channel configs) at
// rest with AES-256-GCM. The key is derived from APP_SECRET with
// HKDF-SHA256; ciphertexts carry a "v1:" version prefix so the scheme can
// change later without ambiguity.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownVersion = errors.New("secret: unknown ciphertext version")
	ErrCorrupt        = errors.New("secret: ciphertext corrupt or wrong key")
)

const (
	prefixV1 = "v1:"
	infoV1   = "upsera channel secrets v1"
)

// Box encrypts and decrypts with one derived key. It is safe for
// concurrent use.
type Box struct{ aead cipher.AEAD }

// New derives the key from appSecret.
func New(appSecret string) (*Box, error) {
	key, err := hkdf.Key(sha256.New, []byte(appSecret), nil, infoV1, 32)
	if err != nil {
		return nil, fmt.Errorf("secret: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns "v1:" + base64url(nonce || ciphertext+tag).
func (b *Box) Encrypt(plain []byte) (string, error) {
	ns := b.aead.NonceSize()
	buf := make([]byte, ns, ns+len(plain)+b.aead.Overhead())
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("secret: nonce: %w", err)
	}
	buf = b.aead.Seal(buf, buf[:ns], plain, nil)
	return prefixV1 + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Decrypt reverses Encrypt. It returns ErrUnknownVersion for a value
// without the "v1:" prefix and ErrCorrupt for anything that does not
// authenticate under this key.
func (b *Box) Decrypt(s string) ([]byte, error) {
	enc, ok := strings.CutPrefix(s, prefixV1)
	if !ok {
		return nil, ErrUnknownVersion
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	ns := b.aead.NonceSize()
	if err != nil || len(raw) < ns+b.aead.Overhead() {
		return nil, ErrCorrupt
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}
