package audit

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"tars/internal/secret"
)

type Entry struct {
	ClientKey string
	SessionID string
	Action    string
	Decision  string
	Args      any
	Result    any
}

func Record(db *sql.DB, e Entry) {
	argsJSON, _ := json.Marshal(e.Args)
	resultJSON, _ := json.Marshal(e.Result)
	args := secret.Redact(string(argsJSON))
	result := secret.Redact(string(resultJSON))
	_, _ = db.Exec(
		`INSERT INTO audit (id, client_key, session_id, action, decision, args, result, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), e.ClientKey, e.SessionID, e.Action, e.Decision, args, result, time.Now().Unix(),
	)
}
