package quota

import (
	"database/sql"
	"errors"
	"time"

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
	if active >= c.cfg.Quota.Global.MaxActiveSessions {
		return errors.New("max active sessions exceeded")
	}
	dayStart := time.Now().Truncate(24 * time.Hour).Unix()
	var today int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM session WHERE key_id = ? AND time_created >= ?`, keyID, dayStart).Scan(&today); err != nil {
		return err
	}
	if today >= c.cfg.Quota.PerKey.MaxSessionsPerDay {
		return errors.New("max sessions per day exceeded")
	}
	return nil
}

func (c *Checker) CheckTurn(keyID string) error {
	var running int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM session WHERE key_id = ? AND status = 'running'`, keyID).Scan(&running); err != nil {
		return err
	}
	if running >= c.cfg.Quota.PerKey.MaxConcurrentTurns {
		return errors.New("max concurrent turns exceeded")
	}
	return nil
}
