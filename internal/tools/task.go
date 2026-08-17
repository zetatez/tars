package tools

import (
	"context"
	"errors"
)

func taskTool() *Tool {
	return &Tool{
		Name:        "task",
		Description: "委派一个子 agent 在独立隔离的会话中完成一个多步任务，返回其结果。适合需要独立探索/执行的子任务。参数：prompt（任务描述，必填）、cwd、model。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string", "description": "子任务描述"},
				"cwd":    map[string]any{"type": "string", "description": "子会话工作目录，默认继承"},
				"model":  map[string]any{"type": "string", "description": "子会话模型，默认继承"},
			},
			"required": []string{"prompt"},
		},
		PolicyAction: "task",
		ParallelSafe: false,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			if sc.Delegate == nil {
				return nil, errors.New("task delegation unavailable")
			}
			prompt, _ := args["prompt"].(string)
			if prompt == "" {
				return nil, errors.New("prompt required")
			}
			cwd, _ := args["cwd"].(string)
			if cwd == "" {
				cwd = sc.Cwd
			}
			model, _ := args["model"].(string)
			text, err := sc.Delegate(ctx, prompt, cwd, model, sc.KeyID, sc.Role)
			if err != nil {
				return nil, err
			}
			return Result{"result": text}, nil
		},
	}
}
