package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/scrypt"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	scryptN = 32768
	scryptR = 8
	scryptP = 1
	saltLen = 16
	keyLen  = 32
)

var (
	ErrUnauthorized = errors.New("unauthorized")
)

type KeyInfo struct {
	KeyID string
	Role  string
}

func CreateKey(db *sql.DB, role string) (plaintext, keyID string, err error) {
	keyID = uuid.NewString()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}
	hash, err := scrypt.Key(secret, salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return "", "", err
	}
	keyHash := hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash)
	plaintext = keyID + "_" + hex.EncodeToString(secret)

	_, err = db.Exec(
		`INSERT INTO api_keys (key_id, key_hash, role, active, created) VALUES (?, ?, ?, 1, unixepoch())`,
		keyID, keyHash, role,
	)
	if err != nil {
		return "", "", err
	}
	return plaintext, keyID, nil
}

func Authenticate(db *sql.DB, r *http.Request) (KeyInfo, error) {
	key := extractKey(r)
	if key == "" {
		return KeyInfo{}, ErrUnauthorized
	}
	return AuthenticateKey(db, key)
}

func AuthenticateKey(db *sql.DB, key string) (KeyInfo, error) {
	idx := strings.IndexByte(key, '_')
	if idx <= 0 {
		return KeyInfo{}, ErrUnauthorized
	}
	keyID := key[:idx]
	secretHex := key[idx+1:]
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return KeyInfo{}, ErrUnauthorized
	}

	var keyHash, role string
	var active int
	err = db.QueryRow(
		`SELECT key_hash, role, active FROM api_keys WHERE key_id = ?`,
		keyID,
	).Scan(&keyHash, &role, &active)
	if err != nil || active != 1 {
		return KeyInfo{}, ErrUnauthorized
	}

	parts := strings.SplitN(keyHash, ":", 2)
	if len(parts) != 2 {
		return KeyInfo{}, ErrUnauthorized
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return KeyInfo{}, ErrUnauthorized
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return KeyInfo{}, ErrUnauthorized
	}
	got, err := scrypt.Key(secret, salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return KeyInfo{}, ErrUnauthorized
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return KeyInfo{}, ErrUnauthorized
	}
	return KeyInfo{KeyID: keyID, Role: role}, nil
}

func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if h := r.Header.Get("X-API-Key"); h != "" {
		return h
	}
	return ""
}

// EnsureAdmin 在库中无 admin 时，将 config 提供的 key 注册为 admin。
// key 格式：<key_id>_<hex secret>。不生成、不从环境变量读取。
func EnsureAdmin(db *sql.DB, adminKey string) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE role = 'admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 || adminKey == "" {
		return nil
	}
	idx := strings.IndexByte(adminKey, '_')
	if idx <= 0 {
		return errors.New("invalid admin key format, want <key_id>_<hex secret>")
	}
	keyID := adminKey[:idx]
	secret, err := hex.DecodeString(adminKey[idx+1:])
	if err != nil {
		return errors.New("invalid admin key format, want <key_id>_<hex secret>")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := scrypt.Key(secret, salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return err
	}
	keyHash := hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash)
	_, err = db.Exec(
		`INSERT INTO api_keys (key_id, key_hash, role, active, created) VALUES (?, ?, 'admin', 1, unixepoch())`,
		keyID, keyHash,
	)
	return err
}
