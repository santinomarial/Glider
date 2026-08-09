// Package secret provides authenticated encryption for secret payloads before
// they cross the etcd persistence boundary.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/santinomarial/glider/internal/api"
)

type Envelope struct {
	APIVersion string       `json:"apiVersion"`
	Metadata   api.Metadata `json:"metadata"`
	KeyVersion int          `json:"key_version"`
	Nonce      []byte       `json:"nonce"`
	Ciphertext []byte       `json:"ciphertext"`
	PayloadMAC []byte       `json:"payload_mac"`
}

type Cipher struct {
	aead    cipher.AEAD
	macKey  [32]byte
	context string
}

func LoadKeyFile(path, context string) (*Cipher, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat secret encryption key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secret encryption key must not be accessible by group or others")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret encryption key: %w", err)
	}
	return NewCipher(key, context)
}

func NewCipher(key []byte, context string) (*Cipher, error) {
	if len(key) != 32 || context == "" {
		return nil, errors.New("secret cipher requires a 32-byte key and non-empty cluster context")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	macKey := sha256.Sum256(append([]byte("glider-secret-mac\x00"), key...))
	return &Cipher{aead: aead, macKey: macKey, context: context}, nil
}

func (c *Cipher) Encrypt(value api.Secret) (Envelope, error) {
	payload, err := json.Marshal(value.Data)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, err
	}
	aad := []byte(c.context + "\x00" + value.Metadata.ID)
	ciphertext := c.aead.Seal(nil, nonce, payload, aad)
	mac := hmac.New(sha256.New, c.macKey[:])
	_, _ = mac.Write(aad)
	_, _ = mac.Write(payload)
	metadata := value.Metadata
	metadata.Revision = 0
	return Envelope{APIVersion: api.Version, Metadata: metadata, KeyVersion: 1, Nonce: nonce, Ciphertext: ciphertext, PayloadMAC: mac.Sum(nil)}, nil
}

func (c *Cipher) Decrypt(value Envelope) (api.Secret, error) {
	if value.KeyVersion != 1 || len(value.Nonce) != c.aead.NonceSize() {
		return api.Secret{}, errors.New("unsupported or invalid secret envelope")
	}
	aad := []byte(c.context + "\x00" + value.Metadata.ID)
	payload, err := c.aead.Open(nil, value.Nonce, value.Ciphertext, aad)
	if err != nil {
		return api.Secret{}, errors.New("secret authentication failed")
	}
	mac := hmac.New(sha256.New, c.macKey[:])
	_, _ = mac.Write(aad)
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), value.PayloadMAC) {
		return api.Secret{}, errors.New("secret payload MAC failed")
	}
	var data map[string][]byte
	if err := json.Unmarshal(payload, &data); err != nil {
		return api.Secret{}, errors.New("invalid encrypted secret payload")
	}
	return api.Secret{APIVersion: api.Version, Metadata: value.Metadata, Data: data}, nil
}
