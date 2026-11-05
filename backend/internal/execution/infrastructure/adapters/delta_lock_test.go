package adapters

// W1-12 fix: the discovery delta lock key is a stable 64-bit hash of
// (version_id, wave_id). Two cooperative processes must compute the same key
// for the same wave and different keys for different waves/versions.

import "testing"

func TestDiscoveryDeltaLockKeyIsStable(t *testing.T) {
	key := discoveryDeltaLockKey(7, 3)
	if key == 0 {
		t.Fatal("lock key must not be zero")
	}
	if again := discoveryDeltaLockKey(7, 3); again != key {
		t.Fatalf("lock key changed between calls: %d != %d", key, again)
	}
	if other := discoveryDeltaLockKey(7, 4); other == key {
		t.Fatalf("lock key for wave 4 equals wave 3: %d", key)
	}
	if other := discoveryDeltaLockKey(8, 3); other == key {
		t.Fatalf("lock key for version 8 equals version 7: %d", key)
	}
	t.Logf("discovery delta lock key(7,3)=%d key(7,4)=%d key(8,3)=%d",
		key, discoveryDeltaLockKey(7, 4), discoveryDeltaLockKey(8, 3))
}
