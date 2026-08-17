package session

import (
	"strings"
	"testing"

	"tars/internal/config"
)

func TestRunSyncDepthLimit(t *testing.T) {
	m := NewManager(nil, nil, "", nil, "", config.Session{}, nil, "interrupt")
	_, err := m.RunSync("k", "", "", "user", "x", MaxAgentDepth+1)
	if err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("expected max nesting depth error, got %v", err)
	}
}
