package identityinfra_test

import (
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure"
	"golang.org/x/crypto/bcrypt"
)

func TestHasherUsesCost12AndVerifies(t *testing.T) {
	hasher := identityinfra.NewHasher(12)

	hash, err := hasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != 12 {
		t.Fatalf("cost = %d, want 12", cost)
	}
	if strings.Contains(hash, "correct-horse-battery") {
		t.Fatal("hash contains the plaintext password")
	}
	if !hasher.Verify(hash, "correct-horse-battery") {
		t.Fatal("correct password rejected")
	}
	if hasher.Verify(hash, "wrong-password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHasherPrehashAvoidsBcrypt72ByteTruncation(t *testing.T) {
	hasher := identityinfra.NewHasher(12)

	prefix := strings.Repeat("a", 80)
	first := prefix + "X"
	second := prefix + "Y"
	firstHash, err := hasher.Hash(first)
	if err != nil {
		t.Fatalf("Hash first: %v", err)
	}
	secondHash, err := hasher.Hash(second)
	if err != nil {
		t.Fatalf("Hash second: %v", err)
	}
	if firstHash == secondHash {
		t.Fatal("passwords differing after 72 bytes produced the same hash")
	}
	if !hasher.Verify(firstHash, first) {
		t.Fatal("first long password rejected")
	}
	if hasher.Verify(firstHash, second) {
		t.Fatal("second long password accepted by the first hash (bcrypt truncation)")
	}
}

func TestHasherDefaultCost(t *testing.T) {
	hasher := identityinfra.NewHasher(0)
	hash, err := hasher.Hash("default-cost-password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != 12 {
		t.Fatalf("default cost = %d, want 12", cost)
	}
}
