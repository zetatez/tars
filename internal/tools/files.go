package tools

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func resolvePath(sc *Scope, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(sc.Cwd, p)
}

func readFileTool() *Tool {
	return &Tool{
		Name:        "read_file",
		Description: "读取文件内容（文本或二进制）。返回文件内容。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "文件路径"},
			},
			"required": []string{"path"},
		},
		PolicyAction: "read_file",
		ResourceKey:  "path",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			p, _ := args["path"].(string)
			if p == "" {
				return nil, errors.New("path required")
			}
			b, err := os.ReadFile(resolvePath(sc, p))
			if err != nil {
				return nil, err
			}
			truncated := false
			max := sc.Cfg.Tools.Exec.MaxOutput
			if max > 0 && len(b) > max {
				b = b[:max]
				truncated = true
			}
			return Result{"content": string(b), "size": len(b), "truncated": truncated}, nil
		},
	}
}

func writeFileTool() *Tool {
	return &Tool{
		Name:        "write_file",
		Description: "写入文件（覆盖或创建）。父目录不存在时报错。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "文件路径"},
				"content": map[string]any{"type": "string", "description": "文件内容"},
			},
			"required": []string{"path", "content"},
		},
		PolicyAction: "write_file",
		ResourceKey:  "path",
		ParallelSafe: false,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			p, _ := args["path"].(string)
			c, _ := args["content"].(string)
			if p == "" {
				return nil, errors.New("path required")
			}
			full := resolvePath(sc, p)
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				return nil, err
			}
			return Result{"path": full, "written": len(c)}, nil
		},
	}
}

func editFileTool() *Tool {
	return &Tool{
		Name:        "edit_file",
		Description: "编辑文件：将第一次出现的 old_string 替换为 new_string。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "文件路径"},
				"old_string": map[string]any{"type": "string"},
				"new_string": map[string]any{"type": "string"},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
		PolicyAction: "edit_file",
		ResourceKey:  "path",
		ParallelSafe: false,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			p, _ := args["path"].(string)
			oldS, _ := args["old_string"].(string)
			newS, _ := args["new_string"].(string)
			if p == "" || oldS == "" {
				return nil, errors.New("path and old_string required")
			}
			full := resolvePath(sc, p)
			b, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			s := string(b)
			if !strings.Contains(s, oldS) {
				return nil, errors.New("old_string not found")
			}
			s = strings.Replace(s, oldS, newS, 1)
			if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
				return nil, err
			}
			return Result{"path": full, "replaced": true}, nil
		},
	}
}

func grepTool() *Tool {
	return &Tool{
		Name:        "grep",
		Description: "在目录下递归搜索匹配文本的文件行。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "要匹配的字符串"},
				"path":    map[string]any{"type": "string", "description": "搜索目录，默认 session cwd"},
				"max":     map[string]any{"type": "integer", "description": "最多返回匹配数，默认 50"},
			},
			"required": []string{"pattern"},
		},
		PolicyAction: "grep",
		ResourceKey:  "path",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return nil, errors.New("pattern required")
			}
			root := sc.Cwd
			if v, _ := args["path"].(string); v != "" {
				root = resolvePath(sc, v)
			}
			max := 50
			if v, ok := args["max"].(float64); ok {
				max = int(v)
			}
			type match struct {
				File string `json:"file"`
				Line int    `json:"line"`
				Text string `json:"text"`
			}
			matches := []match{}
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil || len(matches) >= max {
					return nil
				}
				if info.IsDir() || info.Size() > 1024*1024 {
					return nil
				}
				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				defer f.Close()
				scanner := bufio.NewScanner(f)
				scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
				lineNo := 0
				for scanner.Scan() && len(matches) < max {
					lineNo++
					if strings.Contains(scanner.Text(), pattern) {
						matches = append(matches, match{File: path, Line: lineNo, Text: scanner.Text()})
					}
				}
				return nil
			})
			return Result{"matches": matches, "count": len(matches)}, nil
		},
	}
}

func globTool() *Tool {
	return &Tool{
		Name:        "glob",
		Description: "按 glob 模式匹配文件路径（如 **/*.go）。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		},
		PolicyAction: "glob",
		ResourceKey:  "pattern",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return nil, errors.New("pattern required")
			}
			matches, err := filepath.Glob(resolvePath(sc, pattern))
			if err != nil {
				return nil, err
			}
			return Result{"matches": matches, "count": len(matches)}, nil
		},
	}
}

func lsTool() *Tool {
	return &Tool{
		Name:        "ls",
		Description: "列目录内容。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "目录，默认 session cwd"},
			},
		},
		PolicyAction: "ls",
		ResourceKey:  "path",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			dir := sc.Cwd
			if v, _ := args["path"].(string); v != "" {
				dir = resolvePath(sc, v)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			type entry struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
				Size  int64  `json:"size"`
			}
			out := []entry{}
			for _, e := range entries {
				info, _ := e.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				out = append(out, entry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
			}
			return Result{"entries": out, "count": len(out)}, nil
		},
	}
}
