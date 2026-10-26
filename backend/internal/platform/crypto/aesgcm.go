// Package crypto provides authenticated encryption for secrets stored at rest.
// It is domain-agnostic: callers bind an encrypted value to a field by passing
// the field's additional authenticated data (AAD), so a ciphertext copied from
// one column into another fails to decrypt.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// nonceSize is the AES-GCM nonce size required by the design: 12 random bytes
// prepended to the ciphertext, so the stored form is nonce || ciphertext.
const nonceSize = 12

// keySize is the AES-256 key length enforced by NewKeyring.
const keySize = 32

var (
	// ErrUnknownKeyVersion means the record was encrypted with a key version
	// that is not configured in this process. The configuration cannot be used
	// until the matching key is restored.
	ErrUnknownKeyVersion = errors.New("crypto: unknown key version")
	// ErrInvalidCiphertext means the value is too short to contain a nonce or
	// it failed authentication (tampered, wrong key or wrong AAD).
	ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")
)

// Keyring encrypts with the active key version and decrypts with any configured
// version, so rotating the active key keeps older records readable.
type Keyring struct {
	aeads  map[int]cipher.AEAD
	active int
}

// NewKeyring builds a keyring from a map of key version to 32-byte AES-256 key
// and selects the version used for new ciphertexts. Every key must be 32 bytes,
// versions must be positive and the active version must be present.
func NewKeyring(keys map[int][]byte, active int) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("crypto: keyring requires at least one key")
	}
	aeads := make(map[int]cipher.AEAD, len(keys))
	for version, key := range keys {
		if version <= 0 {
			return nil, fmt.Errorf("crypto: key version must be positive, got %d", version)
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("crypto: key version %d must be %d bytes, got %d", version, keySize, len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("crypto: key version %d: %w", version, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("crypto: key version %d: %w", version, err)
		}
		aeads[version] = aead
	}
	if _, ok := aeads[active]; !ok {
		return nil, fmt.Errorf("crypto: active key version %d is not configured", active)
	}
	return &Keyring{aeads: aeads, active: active}, nil
}

// ActiveVersion returns the key version used for new ciphertexts.
func (k *Keyring) ActiveVersion() int {
	return k.active
}

// Encrypt encrypts plaintext with the active key version and no AAD. Values
// that must be bound to a field use EncryptWithAAD instead.
func (k *Keyring) Encrypt(plaintext []byte) ([]byte, int, error) {
	return k.EncryptWithAAD(plaintext, nil)
}

// Decrypt decrypts a ciphertext produced with the given key version and no AAD.
func (k *Keyring) Decrypt(ciphertext []byte, version int) ([]byte, error) {
	return k.DecryptWithAAD(ciphertext, version, nil)
}

// EncryptWithAAD encrypts plaintext with the active key version and binds the
// ciphertext to aad. It returns the stored form nonce || ciphertext and the key
// version needed to decrypt it.
func (k *Keyring) EncryptWithAAD(plaintext, aad []byte) ([]byte, int, error) {
	aead, ok := k.aeads[k.active]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %d", ErrUnknownKeyVersion, k.active)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, plaintext, aad)
	ciphertext := make([]byte, 0, nonceSize+len(sealed))
	ciphertext = append(ciphertext, nonce...)
	ciphertext = append(ciphertext, sealed...)
	return ciphertext, k.active, nil
}

// DecryptWithAAD decrypts a ciphertext with the given key version and AAD. A
// version that is not configured returns ErrUnknownKeyVersion; malformed or
// tampered values return ErrInvalidCiphertext.
func (k *Keyring) DecryptWithAAD(ciphertext []byte, version int, aad []byte) ([]byte, error) {
	aead, ok := k.aeads[version]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownKeyVersion, version)
	}
	if len(ciphertext) < nonceSize+aead.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	nonce := ciphertext[:nonceSize]
	plaintext, err := aead.Open(nil, nonce, ciphertext[nonceSize:], aad)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrInvalidCiphertext)
	}
	return plaintext, nil
}
