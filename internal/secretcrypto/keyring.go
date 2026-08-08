// Package secretcrypto provides authenticated encryption for control-plane
// secrets. It has no persistence or network dependencies and is safe to call
// while holding a database row lock.
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	EnvelopeVersion = 1
	AlgorithmAESGCM = "AES-256-GCM"
)

var ErrOpen = errors.New("secret ciphertext authentication failed")

type Cipher interface {
	Seal(plaintext, additionalData []byte) (domain.SecretEnvelope, error)
	Open(domain.SecretEnvelope, []byte) ([]byte, error)
}

// AESGCMKeyring encrypts with one active key while retaining older keys for
// decryption during online rotation. Every key must be exactly 32 bytes.
type AESGCMKeyring struct {
	activeKeyID string
	keys        map[string][]byte
	random      io.Reader
}

func NewAESGCMKeyring(activeKeyID string, keys map[string][]byte) (*AESGCMKeyring, error) {
	return newAESGCMKeyring(activeKeyID, keys, rand.Reader)
}

func newAESGCMKeyring(activeKeyID string, keys map[string][]byte, random io.Reader) (*AESGCMKeyring, error) {
	if !validKeyID(activeKeyID) {
		return nil, errors.New("secret keyring active_key_id must be valid UTF-8 and cannot be empty or contain NUL")
	}
	if len(keys) == 0 {
		return nil, errors.New("secret keyring must contain at least one key")
	}
	cloned := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if !validKeyID(id) {
			return nil, errors.New("secret keyring contains a key id that is empty, invalid UTF-8, or contains NUL")
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("secret keyring key %q must decode to 32 bytes", id)
		}
		cloned[id] = append([]byte(nil), key...)
	}
	if _, ok := cloned[activeKeyID]; !ok {
		return nil, errors.New("secret keyring active key is missing")
	}
	if random == nil {
		return nil, errors.New("secret keyring random source is required")
	}
	return &AESGCMKeyring{activeKeyID: activeKeyID, keys: cloned, random: random}, nil
}

func validKeyID(id string) bool {
	return id != "" && utf8.ValidString(id) && !strings.ContainsRune(id, '\x00')
}

func (k *AESGCMKeyring) Seal(plaintext, additionalData []byte) (domain.SecretEnvelope, error) {
	aead, err := newGCM(k.keys[k.activeKeyID])
	if err != nil {
		return domain.SecretEnvelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(k.random, nonce); err != nil {
		return domain.SecretEnvelope{}, fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, additionalData)
	return domain.SecretEnvelope{
		Version: EnvelopeVersion, Algorithm: AlgorithmAESGCM,
		KeyID: k.activeKeyID, Nonce: nonce, Ciphertext: ciphertext,
	}, nil
}

func (k *AESGCMKeyring) Open(envelope domain.SecretEnvelope, additionalData []byte) ([]byte, error) {
	if envelope.Version != EnvelopeVersion || envelope.Algorithm != AlgorithmAESGCM {
		return nil, ErrOpen
	}
	key, ok := k.keys[envelope.KeyID]
	if !ok {
		return nil, ErrOpen
	}
	aead, err := newGCM(key)
	if err != nil || len(envelope.Nonce) != aead.NonceSize() {
		return nil, ErrOpen
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, additionalData)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Zero best-effort clears mutable secret buffers once callers no longer need
// them. Go strings and compiler copies cannot provide a hard zeroization
// guarantee, so public code must still avoid logging or retaining secrets.
func Zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
