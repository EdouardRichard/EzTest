package crypto_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/crypto"
)

func TestKeyringRoundTripAndVersions(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32), 2: bytes.Repeat([]byte{2}, 32)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	ct, v, err := kr.Encrypt([]byte("secret"))
	if err != nil || v != 2 {
		t.Fatalf("v=%d err=%v", v, err)
	}
	pt, err := kr.Decrypt(ct, v)
	if err != nil || string(pt) != "secret" {
		t.Fatalf("pt=%q err=%v", pt, err)
	}
	if _, err := kr.Decrypt(ct, 9); err == nil {
		t.Fatal("missing key version should fail")
	}
}

func TestKeyringEncryptsWithActiveVersionAndDecryptsOldVersions(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32), 2: bytes.Repeat([]byte{2}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ct, version, err := kr.Encrypt([]byte("old-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version=%d want active 1", version)
	}
	pt, err := kr.Decrypt(ct, 1)
	if err != nil || string(pt) != "old-secret" {
		t.Fatalf("pt=%q err=%v", pt, err)
	}
	if _, err := kr.Decrypt(ct, 2); err == nil {
		t.Fatal("decrypting a version-1 ciphertext as version 2 should fail")
	}
}

func TestKeyringNonceAndTamperDetection(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := kr.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := kr.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two encryptions of the same plaintext must use distinct nonces")
	}
	// 12-byte nonce followed by the GCM ciphertext and 16-byte tag.
	if want := 12 + len("secret") + 16; len(first) != want {
		t.Fatalf("ciphertext length=%d want %d", len(first), want)
	}

	tampered := append([]byte(nil), first...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := kr.Decrypt(tampered, 1); err == nil {
		t.Fatal("tampered ciphertext must not decrypt")
	}
	if _, err := kr.Decrypt(first[:12], 1); err == nil {
		t.Fatal("nonce-only ciphertext must not decrypt")
	}
}

func TestKeyringAADIsBoundPerField(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	apiKeyAAD := []byte("eztest.llm.api_key")
	headersAAD := []byte("eztest.llm.headers")

	ct, version, err := kr.EncryptWithAAD([]byte("secret"), apiKeyAAD)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := kr.DecryptWithAAD(ct, version, apiKeyAAD)
	if err != nil || string(pt) != "secret" {
		t.Fatalf("pt=%q err=%v", pt, err)
	}
	if _, err := kr.DecryptWithAAD(ct, version, headersAAD); err == nil {
		t.Fatal("ciphertext encrypted for one AAD must not decrypt under another")
	}
	if _, err := kr.Decrypt(ct, version); err == nil {
		t.Fatal("AAD-bound ciphertext must not decrypt without AAD")
	}
}

func TestNewKeyringRejectsInvalidKeys(t *testing.T) {
	cases := []struct {
		name   string
		keys   map[int][]byte
		active int
	}{
		{name: "empty", keys: map[int][]byte{}, active: 1},
		{name: "short key", keys: map[int][]byte{1: bytes.Repeat([]byte{1}, 16)}, active: 1},
		{name: "missing active", keys: map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, active: 2},
		{name: "non-positive version", keys: map[int][]byte{0: bytes.Repeat([]byte{1}, 32)}, active: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := crypto.NewKeyring(tc.keys, tc.active); err == nil {
				t.Fatal("expected an invalid keyring configuration error")
			}
		})
	}
}

func TestKeyringUnknownVersionIsExplicit(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ct, version, err := kr.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Decrypt(ct, version+1); !errors.Is(err, crypto.ErrUnknownKeyVersion) {
		t.Fatalf("err=%v, want ErrUnknownKeyVersion", err)
	}
}

func TestKeyringActiveVersion(t *testing.T) {
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32), 2: bytes.Repeat([]byte{2}, 32)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := kr.ActiveVersion(); got != 2 {
		t.Fatalf("ActiveVersion()=%d want 2", got)
	}
}
