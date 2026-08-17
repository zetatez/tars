package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func applyPatchTool() *Tool {
	return &Tool{
		Name: "apply_patch",
		Description: "用补丁批量修改文件。格式：\n*** Begin Patch\n*** Update File: <path>\n@@\n context 行\n-old line\n+new line\n*** Update File: <path2>\n@@\n+added line\n*** End Patch\n" +
			"行以空格开头=上下文，-开头=删除，+开头=添加。文件不存在时自动创建（仅含添加行）。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]any{"type": "string", "description": "补丁文本"},
			},
			"required": []string{"patch"},
		},
		PolicyAction: "write_file",
		ParallelSafe: false,
		Execute:      runApplyPatch,
	}
}

func runApplyPatch(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
	patch, _ := args["patch"].(string)
	if patch == "" {
		return nil, errors.New("patch required")
	}
	files, err := ParsePatch(patch)
	if err != nil {
		return nil, err
	}
	applied := map[string]string{}
	for _, fp := range files {
		path := resolvePath(sc, fp.Path)
		var lines []string
		if b, err := os.ReadFile(path); err == nil {
			lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		for _, h := range fp.Hunks {
			changed := false
			if len(h.Pattern) == 0 {
				lines = append(lines, h.Addition...)
				changed = true
			} else {
				var nl []string
				var ok bool
				nl, ok = applyHunk(lines, h)
				if !ok {
					return nil, errors.New("apply_patch: hunk not found in " + path)
				}
				lines = nl
				changed = true
			}
			_ = changed
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		applied[path] = fp.Path
	}
	return Result{"applied": len(applied), "files": applied}, nil
}

type FilePatch struct {
	Path  string
	Hunks []Hunk
}

type Hunk struct {
	Pattern  []string // 上下文 + 删除行（要匹配删除的部分）
	Addition []string // 上下文 + 添加行（替换后的内容）
}

func ParsePatch(patch string) ([]FilePatch, error) {
	lines := strings.Split(patch, "\n")
	var files []FilePatch
	var cur *FilePatch

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case trimmed == "*** Begin Patch":
			continue
		case trimmed == "*** End Patch":
			if cur != nil {
				files = append(files, *cur)
			}
			return files, nil
		case strings.HasPrefix(trimmed, "*** Update File: "):
			if cur != nil {
				files = append(files, *cur)
			}
			cur = &FilePatch{Path: strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File: "))}
		case trimmed == "@@":
			if cur != nil {
				cur.Hunks = append(cur.Hunks, Hunk{})
			}
		case cur == nil || trimmed == "":
			continue
		default:
			if len(cur.Hunks) == 0 {
				cur.Hunks = append(cur.Hunks, Hunk{})
			}
			h := &cur.Hunks[len(cur.Hunks)-1]
			switch trimmed[0] {
			case '-':
				h.Pattern = append(h.Pattern, trimmed[1:])
			case '+':
				h.Addition = append(h.Addition, trimmed[1:])
			case ' ':
				h.Pattern = append(h.Pattern, trimmed[1:])
				h.Addition = append(h.Addition, trimmed[1:])
			default:
				return nil, errors.New("apply_patch: invalid line: " + trimmed)
			}
		}
	}
	if cur != nil {
		files = append(files, *cur)
	}
	return files, nil
}

func applyHunk(lines []string, h Hunk) ([]string, bool) {
	n := len(h.Pattern)
	if n == 0 {
		return nil, false
	}
	for i := 0; i <= len(lines)-n; i++ {
		match := true
		for j, p := range h.Pattern {
			if lines[i+j] != p {
				match = false
				break
			}
		}
		if match {
			out := append([]string{}, lines[:i]...)
			out = append(out, h.Addition...)
			out = append(out, lines[i+n:]...)
			return out, true
		}
	}
	return nil, false
}
