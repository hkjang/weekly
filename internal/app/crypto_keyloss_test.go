package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The state volume holds instance.key when WEEKLY_ENCRYPTION_KEY is not set.
// Losing that file used to mint a replacement key silently, which orphaned
// every stored secret while the service reported itself healthy.

func TestLoadOrCreateInstanceKeyRefusesToReplaceALostKey(t *testing.T) {
	withTemporaryStateDirectory(t)
	if _, err := loadOrCreateInstanceKey(false); !errors.Is(err, errSecretKeyMissing) {
		t.Fatalf("expected errSecretKeyMissing when secrets exist and the key is gone, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDirectory, "instance.key")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a replacement key must not be written when the caller forbids it")
	}
}

func TestLoadOrCreateInstanceKeyCreatesOnFirstInstall(t *testing.T) {
	withTemporaryStateDirectory(t)
	created, err := loadOrCreateInstanceKey(true)
	if err != nil {
		t.Fatalf("first install must be able to create a key: %v", err)
	}
	if len(created) != 32 {
		t.Fatalf("key length = %d, want 32", len(created))
	}
	// A second start must reuse the stored key, not mint another one.
	reused, err := loadOrCreateInstanceKey(false)
	if err != nil {
		t.Fatalf("an existing key must load even when creation is forbidden: %v", err)
	}
	if string(reused) != string(created) {
		t.Error("the stored key must be reused across restarts")
	}
}

func TestLoadSecretBoxesSurfacesTheMissingKey(t *testing.T) {
	withTemporaryStateDirectory(t)
	if _, _, _, err := loadSecretBoxes("", false); !errors.Is(err, errSecretKeyMissing) {
		t.Fatalf("expected errSecretKeyMissing, got %v", err)
	}
	// An environment key makes the state volume irrelevant for decryption.
	active, legacy, source, err := loadSecretBoxes("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", false)
	if err != nil {
		t.Fatalf("an environment key must work without the state volume: %v", err)
	}
	if source != "environment" || active == nil || legacy != nil {
		t.Errorf("source=%q active=%v legacy=%v, want an environment key with no legacy box", source, active != nil, legacy != nil)
	}
}

func TestSecretRoundTripAcrossKeyRotation(t *testing.T) {
	oldKey, err := newSecretBox([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := newSecretBox([]byte("abcdefghijabcdefghijabcdefghijab"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := oldKey.Encrypt("confluence-password")
	if err != nil {
		t.Fatal(err)
	}
	// Rotating with the previous key available re-encrypts the value.
	rotated, migrated, available, err := reencryptSecret(ciphertext, newKey, oldKey)
	if err != nil || !migrated || !available {
		t.Fatalf("rotation failed: migrated=%v available=%v err=%v", migrated, available, err)
	}
	plain, err := newKey.Decrypt(rotated)
	if err != nil || plain != "confluence-password" {
		t.Fatalf("re-encrypted value did not survive: %q %v", plain, err)
	}
	// Without the previous key the value is reported unavailable, never lost.
	_, _, available, err = reencryptSecret(ciphertext, newKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Error("a value encrypted with a lost key must be reported unavailable")
	}
}

// withTemporaryStateDirectory points the package state directory at a temporary
// path for the duration of one test.
func withTemporaryStateDirectory(t *testing.T) {
	t.Helper()
	original := stateDirectory
	stateDirectory = t.TempDir()
	t.Cleanup(func() { stateDirectory = original })
}
