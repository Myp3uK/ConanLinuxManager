package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// SecretBox encrypts backup passwords before they are stored in SQLite.
// The master key is supplied by the service environment and never stored in DB.
type SecretBox struct{ aead cipher.AEAD }

func NewSecretBox(master string) (*SecretBox, error) {
	if master == "" {
		return nil, fmt.Errorf("secret master key is empty")
	}
	hash := sha256.Sum256([]byte(master))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

func (s *SecretBox) Seal(value string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := s.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func (s *SecretBox) Open(value string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	n := s.aead.NonceSize()
	if len(data) < n {
		return "", fmt.Errorf("encrypted secret is too short")
	}
	plain, err := s.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}
