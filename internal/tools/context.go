package tools

import (
	"context"
)

func contextTool() *Tool {
	return &Tool{
		Name:        "get_context_remaining",
		Description: "返回当前上下文窗口的 token 用量估算与剩余空间，用于判断是否需要压缩历史。",
		Params: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		PolicyAction: "get_context_remaining",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			window := sc.ContextWindow
			if window <= 0 {
				window = 128000
			}
			used := sc.CurrentTokens
			if used < 0 {
				used = 0
			}
			return Result{
				"window":    window,
				"used":      used,
				"remaining": window - used,
			}, nil
		},
	}
}
