package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

// stateDirectory holds the fallback encryption key and uploaded templates. It
// is a variable so tests can redirect it to a temporary path.
var stateDirectory = "/var/lib/weekly"

type secretBox struct{ aead cipher.AEAD }

// errSecretKeyMissing reports that the only key able to decrypt the stored
// secrets is gone. Minting a replacement here would start the service with
// every secret silently orphaned, so the caller must stop instead.
var errSecretKeyMissing = errors.New("secret encryption key is unavailable")

// loadSecretBoxes resolves the active key and, when the key has just changed,
// the previous one so stored secrets can be re-encrypted.
//
// allowNewKey must only be true when losing the current secrets is acceptable:
// either the database holds no encrypted secret yet, or the operator has
// explicitly accepted a reset.
func loadSecretBoxes(configuredKey string, allowNewKey bool) (active, legacy *secretBox, source string, err error) {
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return nil, nil, "", fmt.Errorf("create state directory: %w", err)
	}
	if strings.TrimSpace(configuredKey) != "" {
		key, decodeErr := decodeEncryptionKey(configuredKey)
		if decodeErr != nil {
			return nil, nil, "", decodeErr
		}
		active, err = newSecretBox(key)
		if err != nil {
			return nil, nil, "", err
		}
		fileKey, readErr := readInstanceKey()
		if readErr == nil && !bytes.Equal(fileKey, key) {
			legacy, err = newSecretBox(fileKey)
			if err != nil {
				return nil, nil, "", err
			}
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, nil, "", readErr
		}
		return active, legacy, "environment", nil
	}
	key, err := loadOrCreateInstanceKey(allowNewKey)
	if err != nil {
		return nil, nil, "", err
	}
	active, err = newSecretBox(key)
	return active, nil, "state_volume", err
}

func decodeEncryptionKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, encoding := range encodings {
		key, err := encoding.DecodeString(value)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("WEEKLY_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
}

func newSecretBox(key []byte) (*secretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid instance key length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

func readInstanceKey() ([]byte, error) {
	key, err := os.ReadFile(filepath.Join(stateDirectory, "instance.key"))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid instance key length")
	}
	return key, nil
}

func loadOrCreateInstanceKey(allowNewKey bool) ([]byte, error) {
	key, err := readInstanceKey()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read instance key: %w", err)
	}
	if !allowNewKey {
		// The state volume lost instance.key while the database still holds
		// secrets encrypted with it. Generating a new key here would leave the
		// service running with unusable OIDC, AI and Confluence credentials.
		return nil, errSecretKeyMissing
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDirectory, "instance.key")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readInstanceKey()
	}
	if err != nil {
		return nil, fmt.Errorf("persist instance key: %w", err)
	}
	written, writeErr := file.Write(key)
	if writeErr == nil && written != len(key) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("persist instance key: %w", writeErr)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("persist instance key: %w", err)
	}
	return key, nil
}

type secretMigrationResult struct {
	Migrated    int
	Unavailable []string
}

// countEncryptedSecrets reports how many secret settings currently hold a
// value. It is read before any key is resolved, so the caller can tell a first
// install apart from an existing install whose key has gone missing.
func countEncryptedSecrets(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var total int
	err := db.QueryRow(ctx, `SELECT count(*) FROM app_settings WHERE secret=true AND value<>''`).Scan(&total)
	return total, err
}

type storedSecret struct {
	Key   string
	Value string
}

func migrateSecretSettings(ctx context.Context, db *pgxpool.Pool, active, legacy *secretBox) (secretMigrationResult, error) {
	result := secretMigrationResult{Unavailable: []string{}}
	tx, err := db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT key,value FROM app_settings WHERE secret=true AND value<>'' ORDER BY key`)
	if err != nil {
		return result, err
	}
	values := []storedSecret{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			rows.Close()
			return result, err
		}
		values = append(values, storedSecret{Key: key, Value: value})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	for _, stored := range values {
		encrypted, migrated, available, reencryptErr := reencryptSecret(stored.Value, active, legacy)
		if reencryptErr != nil {
			return result, reencryptErr
		}
		if !available {
			result.Unavailable = append(result.Unavailable, stored.Key)
			continue
		}
		if !migrated {
			continue
		}
		command, updateErr := tx.Exec(ctx, `UPDATE app_settings SET value=$1 WHERE key=$2 AND value=$3`, encrypted, stored.Key, stored.Value)
		if updateErr != nil {
			return result, updateErr
		}
		if command.RowsAffected() == 1 {
			result.Migrated++
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func reencryptSecret(value string, active, legacy *secretBox) (encrypted string, migrated, available bool, err error) {
	if _, err = active.Decrypt(value); err == nil {
		return value, false, true, nil
	}
	if legacy == nil {
		return value, false, false, nil
	}
	plain, err := legacy.Decrypt(value)
	if err != nil {
		return value, false, false, nil
	}
	encrypted, err = active.Encrypt(plain)
	if err != nil {
		return "", false, false, err
	}
	return encrypted, true, true, nil
}

func (s *secretBox) Encrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := s.aead.Seal(nil, nonce, []byte(value), []byte("weekly-setting-v1"))
	combined := append(nonce, ciphertext...)
	return "enc:v1:" + base64.RawURLEncoding.EncodeToString(combined), nil
}

func (s *secretBox) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "enc:v1:") {
		return "", errors.New("unsupported encrypted value")
	}
	combined, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "enc:v1:"))
	if err != nil || len(combined) < s.aead.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	plain, err := s.aead.Open(nil, combined[:s.aead.NonceSize()], combined[s.aead.NonceSize():], []byte("weekly-setting-v1"))
	if err != nil {
		return "", errors.New("cannot decrypt setting")
	}
	return string(plain), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	memory := uint32(64 * 1024)
	iterations := uint32(3)
	parallelism := uint8(2)
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

func numericID() (uint64, error) {
	buf := make([]byte, 8)
	_, err := rand.Read(buf)
	return binary.BigEndian.Uint64(buf), err
}
