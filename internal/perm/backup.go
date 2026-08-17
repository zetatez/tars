package perm

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

func BackupBeforeWrite(db *sql.DB, dataDir, sessionID, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])

	backupFile := filepath.Join(dataDir, "backups", "fs",
		filepath.Base(path)+"."+time.Now().Format("20060102-150405")+"-"+hash[:8])

	if err := os.MkdirAll(filepath.Dir(backupFile), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(backupFile, b, 0o644); err != nil {
		return "", err
	}

	_, _ = db.Exec(
		`INSERT INTO fs_backup (id, session_id, path, backup_path, before_hash, created) VALUES (?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), sessionID, path, backupFile, hash, time.Now().Unix(),
	)
	return hash, nil
}

func Rollback(db *sql.DB, sessionID string) error {
	var backupPath, path string
	err := db.QueryRow(
		`SELECT path, backup_path FROM fs_backup WHERE session_id = ? ORDER BY created DESC LIMIT 1`,
		sessionID,
	).Scan(&path, &backupPath)
	if err != nil {
		return errors.New("no backup found")
	}
	b, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
