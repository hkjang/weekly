package app

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"
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

// Argon2id reserves 64 MiB for the length of every hash and every verification.
// Nobody noticed while logins arrived one at a time; 250 people signing in at
// nine on Monday reserved sixteen gigabytes between them and the container was
// killed on the busiest minute of the week.
//
// The fix is a queue, so this test is about the queue: however many callers
// arrive at once, only a bounded number are inside argon2 together.
func TestPasswordWorkIsBounded(t *testing.T) {
	limit := cap(passwordWork)
	if limit < 2 {
		t.Fatalf("the pool holds %d slots, which cannot bound anything", limit)
	}
	// The ceiling is the whole point: each slot is 64 MiB the container must
	// have, so a bound that grows with the machine would put a 64 core host
	// back where it started.
	if limit > 8 {
		t.Errorf("%d slots reserve %d MiB of argon2 memory at peak", limit, limit*64)
	}

	var mu sync.Mutex
	inside, peak := 0, 0
	var wg sync.WaitGroup
	for caller := 0; caller < limit*6; caller++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			withPasswordSlot(func() bool {
				mu.Lock()
				inside++
				if inside > peak {
					peak = inside
				}
				mu.Unlock()
				time.Sleep(3 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return true
			})
		}()
	}
	wg.Wait()

	if peak > limit {
		t.Errorf("%d callers were inside argon2 at once, above the %d slot bound", peak, limit)
	}
	// Without this the test would also pass on a bound of one, which would make
	// every login wait for every other login on a machine with cores to spare.
	if peak < 2 {
		t.Errorf("only %d ran together; the queue is serialising work it could overlap", peak)
	}
}

// The queue must not leak slots. A verification that returns early — a bad
// password, a malformed hash — still has to give its slot back, or the pool
// drains and every later login blocks forever.
func TestPasswordSlotsAreReturned(t *testing.T) {
	hash, err := hashPassword("WeeklyVerify1234")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < cap(passwordWork)*3; attempt++ {
		verifyPassword(hash, "wrong password")
		verifyPassword("not a hash at all", "wrong password")
	}
	if len(passwordWork) != 0 {
		t.Fatalf("%d slots are still held after only failures", len(passwordWork))
	}
	if !verifyPassword(hash, "WeeklyVerify1234") {
		t.Error("a correct password no longer verifies after repeated failures")
	}
}

// The bound is only worth having if it is sized against the container. On a
// 32 core host a pod limited to one CPU and 512 MiB still opened eight slots
// and reserved the whole limit for argon2, so the guard that existed to prevent
// an out-of-memory kill caused one.
func TestPasswordWorkersFitInsideTheContainerLimit(t *testing.T) {
	workers := cap(passwordWork)
	limit := cgroupMemoryLimit()
	if limit <= 0 {
		// No cgroup limit here, so there is nothing to fit inside. The ceiling
		// still has to hold.
		if workers > 8 {
			t.Errorf("%d slots reserve %d MiB with no limit to answer to", workers, workers*64)
		}
		return
	}
	reserved := int64(workers) * argonBytes
	if reserved+passwordWorkerHeadroom > limit {
		t.Errorf("%d slots reserve %d MiB, which with %d MiB of headroom does not fit a %d MiB limit",
			workers, reserved>>20, passwordWorkerHeadroom>>20, limit>>20)
	}
	if workers < 1 {
		t.Error("a pool of zero can never verify a password")
	}
}

// cgroupMemoryLimit answers 0 rather than guessing when there is no limit to
// read, because a wrong number here silently mis-sizes the queue.
func TestCgroupMemoryLimitReadsOnlyWhatItCanTrust(t *testing.T) {
	limit := cgroupMemoryLimit()
	if limit < 0 {
		t.Errorf("a negative limit (%d) would size the queue below one slot", limit)
	}
	if limit > 0 && limit < argonBytes {
		t.Logf("한도 %d MiB 는 argon2 한 번보다 작아 큐가 최소치로 내려갑니다", limit>>20)
	}
}
