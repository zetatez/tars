package tools

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

func memoryStoreTool() *Tool {
	return &Tool{
		Name:        "memory_store",
		Description: "写入长期记忆（关键结论/事实/流程），供后续会话复用。同 key 覆盖更新。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":        map[string]any{"type": "string", "description": "语义主键，如 '生产变更流程'"},
				"content":    map[string]any{"type": "string", "description": "记忆内容，≤512 字符"},
				"scope":      map[string]any{"type": "string", "enum": []string{"global", "workspace", "session"}},
				"importance": map[string]any{"type": "integer", "minimum": 0, "maximum": 5},
			},
			"required": []string{"key", "content"},
		},
		PolicyAction: "memory_store",
		ParallelSafe: false,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			if sc.KeyID == "" {
				return nil, errors.New("memory_store requires key context")
			}
			key, _ := args["key"].(string)
			content, _ := args["content"].(string)
			if key == "" || content == "" {
				return nil, errors.New("key and content required")
			}
			if len(content) > 512 {
				content = content[:512]
			}
			scope, _ := args["scope"].(string)
			if scope == "" {
				scope = "global"
			}
			importance := 0
			if v, ok := args["importance"].(float64); ok {
				importance = int(v)
			}
			now := time.Now().Unix()
			_, err := sc.DB.Exec(`
				INSERT INTO memory (id, key_id, key, content, scope, session_id, kind, importance, source, time_created, time_updated, time_accessed)
				VALUES (?, ?, ?, ?, ?, ?, 'fact', ?, 'user', ?, ?, ?)
				ON CONFLICT(key_id, scope, key) DO UPDATE SET
					content=excluded.content, importance=excluded.importance,
					time_updated=excluded.time_updated, time_accessed=excluded.time_accessed`,
				uuid.NewString(), sc.KeyID, key, content, scope, sc.SessionID, importance, now, now, now,
			)
			if err != nil {
				return nil, err
			}
			return Result{"stored": true, "key": key, "scope": scope}, nil
		},
	}
}

func memoryQueryTool() *Tool {
	return &Tool{
		Name:        "memory_query",
		Description: "检索记忆。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer"},
			},
			"required": []string{"query"},
		},
		PolicyAction: "memory_query",
		ResourceKey:  "query",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			if sc.KeyID == "" {
				return nil, errors.New("memory_query requires key context")
			}
			q, _ := args["query"].(string)
			if q == "" {
				return nil, errors.New("query required")
			}
			limit := 10
			if v, ok := args["limit"].(float64); ok {
				limit = int(v)
			}
			rows, err := sc.DB.Query(`
				SELECT m.key, m.content, m.scope, m.importance
				FROM memory m JOIN memory_fts f ON f.memory_id = m.id
				WHERE f.memory_fts MATCH ? AND m.key_id = ?
				ORDER BY m.importance DESC, m.time_accessed DESC LIMIT ?`,
				q, sc.KeyID, limit,
			)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			type item struct {
				Key        string `json:"key"`
				Content    string `json:"content"`
				Scope      string `json:"scope"`
				Importance int    `json:"importance"`
			}
			out := []item{}
			for rows.Next() {
				var it item
				if rows.Scan(&it.Key, &it.Content, &it.Scope, &it.Importance) == nil {
					out = append(out, it)
				}
			}
			return Result{"matches": out, "count": len(out)}, nil
		},
	}
}

func taskDoneTool() *Tool {
	return &Tool{
		Name:        "task_done",
		Description: "标记任务完成。当任务已解决时调用，作为最终回复。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "完成总结"},
			},
		},
		PolicyAction: "task_done",
		ParallelSafe: false,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			summary, _ := args["summary"].(string)
			return Result{"done": true, "summary": summary}, nil
		},
	}
}
