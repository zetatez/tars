package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"tars/internal/config"

	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	lockFile *os.File
}

func Open(dataDir string, cfg config.Storage) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data_dir: %w", err)
	}

	lockPath := filepath.Join(dataDir, "tars.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("another tars instance holds the lock: %w", err)
	}

	dbPath := filepath.Join(dataDir, "tars.db")
	busyMs := cfg.BusyTimeout.Duration.Milliseconds()
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(%s)&_pragma=busy_timeout(%d)",
		dbPath, cfg.Synchronous, busyMs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		lockFile.Close()
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, lockFile: lockFile}
	if err := s.migrate(); err != nil {
		db.Close()
		unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		lockFile.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			seq INTEGER NOT NULL, role TEXT NOT NULL,
			content TEXT NOT NULL, created INTEGER NOT NULL,
			UNIQUE(session_id, seq)
		)`,
		`CREATE TABLE IF NOT EXISTS session (
			id TEXT PRIMARY KEY, key_id TEXT NOT NULL,
			cwd TEXT NOT NULL, env TEXT, title TEXT,
			status TEXT NOT NULL, model TEXT,
			time_created INTEGER, time_updated INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			key_id TEXT PRIMARY KEY, key_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			active INTEGER NOT NULL DEFAULT 1,
			created INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS key_config (
			key_id TEXT PRIMARY KEY, config TEXT NOT NULL,
			time_updated INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memory (
			id TEXT PRIMARY KEY, key_id TEXT NOT NULL,
			key TEXT NOT NULL, content TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'session',
			session_id TEXT, kind TEXT NOT NULL DEFAULT 'fact',
			tags TEXT, importance INTEGER DEFAULT 0,
			confidence REAL DEFAULT 1.0, source TEXT DEFAULT 'user',
			ttl INTEGER, embed BLOB,
			time_created INTEGER, time_updated INTEGER, time_accessed INTEGER,
			UNIQUE(key_id, scope, key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_scope ON memory(key_id, scope, importance DESC, time_accessed DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(memory_id, key, content, tags, tokenize='trigram')`,
		`CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
			INSERT INTO memory_fts(memory_id, key, content, tags) VALUES (new.id, new.key, new.content, new.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
			INSERT INTO memory_fts(memory_fts, memory_id, key, content, tags) VALUES ('delete', old.id, old.key, old.content, old.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memory BEGIN
			INSERT INTO memory_fts(memory_fts, memory_id, key, content, tags) VALUES ('delete', old.id, old.key, old.content, old.tags);
			INSERT INTO memory_fts(memory_id, key, content, tags) VALUES (new.id, new.key, new.content, new.tags);
		END`,
		`CREATE TABLE IF NOT EXISTS audit (
			id TEXT PRIMARY KEY, client_key TEXT, session_id TEXT,
			action TEXT, decision TEXT, args TEXT, result TEXT,
			created INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS approval (
			id TEXT PRIMARY KEY, session_id TEXT,
			action TEXT, resource TEXT, status TEXT,
			created INTEGER NOT NULL, resolved INTEGER
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) CheckpointLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		}
	}
}

func (s *Store) Close() error {
	var err error
	if s.db != nil {
		s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		err = s.db.Close()
	}
	if s.lockFile != nil {
		unix.Flock(int(s.lockFile.Fd()), unix.LOCK_UN)
		s.lockFile.Close()
	}
	return err
}
