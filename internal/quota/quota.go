package quota

import (
	"database/sql"
	"errors"

	"tars/internal/config"
)

type Checker struct {
	cfg *config.Config
	db  *sql.DB
}

func New(cfg *config.Config, db *sql.DB) *Checker {
	return &Checker{cfg: cfg, db: db}
}

func (c *Checker) CheckCreateSession(keyID string) error {
	var active int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM session WHERE status != 'archived'`).Scan(&active); err != nil {
		return err
	}
	if active >= c.cfg.Quota.MaxActiveSessions {
		return errors.New("max active sessions exceeded")
	}
	return nil
}

func (c *Checker) CheckTurn(keyID string) error {
	var running int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM session WHERE key_id = ? AND status = 'running'`, keyID).Scan(&running); err != nil {
		return err
	}
	if running >= c.cfg.Quota.MaxConcurrentTurnsPerKey {
		return errors.New("max concurrent turns exceeded")
	}
	return nil
}
