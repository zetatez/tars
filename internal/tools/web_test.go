package tools

import (
	"context"
	"strings"
	"testing"

	"tars/internal/config"
)

func TestWebsearchLimitsResults(t *testing.T) {
	// 构造 maxSearchResults+2 个结果，验证按常量截断并标注 total/truncated
	var results []map[string]string
	for i := 0; i < maxSearchResults+2; i++ {
		results = append(results, map[string]string{
			"title": "T",
			"url":   "http://example.com/",
		})
	}
	out := limitSearchResults("q", results, maxSearchResults)
	got, _ := out["results"].([]map[string]string)
	if len(got) != maxSearchResults {
		t.Fatalf("results limited to %d, want %d", len(got), maxSearchResults)
	}
	if out["total"] != maxSearchResults+2 {
		t.Fatalf("total = %v, want %d", out["total"], maxSearchResults+2)
	}
	if out["truncated"] != true {
		t.Fatal("truncated flag missing")
	}

	// max<=0 时不截断
	out = limitSearchResults("q", results, 0)
	got, _ = out["results"].([]map[string]string)
	if len(got) != maxSearchResults+2 {
		t.Fatalf("max=0 should keep all, got %d", len(got))
	}
	if _, ok := out["truncated"]; ok {
		t.Fatal("no truncated flag when not truncated")
	}
}

func TestWebsearchDisabled(t *testing.T) {
	tool := websearchTool()
	sc := &Scope{Cfg: &config.Config{}}
	_, err := tool.Execute(context.Background(), map[string]any{"query": "x"}, sc)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}
