package app

import (
	"encoding/base64"
	"testing"
)

func TestDecodeEncryptionKeyRequiresExactly32Bytes(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	decoded, err := decodeEncryptionKey(encoded)
	if err != nil || string(decoded) != string(key) {
		t.Fatalf("valid encryption key was rejected: decoded=%x err=%v", decoded, err)
	}
	if _, err := decodeEncryptionKey(base64.StdEncoding.EncodeToString(key[:31])); err == nil {
		t.Fatal("expected a short encryption key to be rejected")
	}
}

func TestReencryptSecretMigratesFromLegacyKey(t *testing.T) {
	legacy, err := newSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	activeKey := make([]byte, 32)
	activeKey[0] = 1
	active, err := newSecretBox(activeKey)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := legacy.Encrypt("durable-secret")
	if err != nil {
		t.Fatal(err)
	}

	migratedValue, migrated, available, err := reencryptSecret(stored, active, legacy)
	if err != nil || !migrated || !available {
		t.Fatalf("secret was not migrated: migrated=%v available=%v err=%v", migrated, available, err)
	}
	plain, err := active.Decrypt(migratedValue)
	if err != nil || plain != "durable-secret" {
		t.Fatalf("migrated secret cannot be decrypted: plain=%q err=%v", plain, err)
	}
}

func TestReencryptSecretReportsMissingRecoveryKey(t *testing.T) {
	oldBox, _ := newSecretBox(make([]byte, 32))
	newKey := make([]byte, 32)
	newKey[0] = 1
	active, _ := newSecretBox(newKey)
	stored, _ := oldBox.Encrypt("lost-secret")
	_, migrated, available, err := reencryptSecret(stored, active, nil)
	if err != nil || migrated || available {
		t.Fatalf("unexpected recovery result: migrated=%v available=%v err=%v", migrated, available, err)
	}
}
