package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	reTag   = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>`)
	reSpace = regexp.MustCompile(`\s+`)
)

// 网络工具输出/数量控制常量（控制上下文 token 消耗）：
//   - maxSearchResults: 单次 websearch 返回的最大结果条数
//   - maxFetchOutput:   单次 webfetch 文本输出的最大字节数
const (
	maxSearchResults = 6
	maxFetchOutput   = 8 * 1024
)

func webfetchTool() *Tool {
	return &Tool{
		Name:        "webfetch",
		Description: "抓取网页内容并提取纯文本。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "目标 URL"},
			},
			"required": []string{"url"},
		},
		PolicyAction: "webfetch",
		ResourceKey:  "url",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			if !sc.Cfg.Network.WebFetch {
				return nil, errors.New("webfetch disabled")
			}
			u, _ := args["url"].(string)
			if u == "" {
				return nil, errors.New("url required")
			}
			timeout := sc.Cfg.Network.ConnectTimeout.Duration
			if timeout <= 0 {
				timeout = 16 * time.Second
			}
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx2, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "tars-agent/0.1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
			if err != nil {
				return nil, err
			}
			text := stripHTML(string(b))
			return Result{"url": u, "status": resp.StatusCode, "text": truncate(text, maxFetchOutput)}, nil
		},
	}
}

func websearchTool() *Tool {
	return &Tool{
		Name:        "websearch",
		Description: "搜索网页（DuckDuckGo html 接口，返回结果标题/链接/摘要）。",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		},
		PolicyAction: "websearch",
		ResourceKey:  "query",
		ParallelSafe: true,
		Execute: func(ctx context.Context, args map[string]any, sc *Scope) (Result, error) {
			if !sc.Cfg.Network.WebSearch {
				return nil, errors.New("websearch disabled")
			}
			q, _ := args["query"].(string)
			if q == "" {
				return nil, errors.New("query required")
			}
			timeout := sc.Cfg.Network.ConnectTimeout.Duration
			if timeout <= 0 {
				timeout = 16 * time.Second
			}
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(q)
			req, err := http.NewRequestWithContext(ctx2, http.MethodGet, u, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "tars-agent/0.1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
			out := limitSearchResults(q, parseDDG(string(b)), maxSearchResults)
			return out, nil
		},
	}
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// limitSearchResults 控制单次搜索返回条数（默认不超过 max），附 total/truncated 信息，
// 避免一次塞入过多链接触发 LLM 逐条抓取浪费 token。
func limitSearchResults(query string, results []map[string]string, max int) Result {
	total := len(results)
	if max > 0 && len(results) > max {
		results = results[:max]
	}
	out := Result{"query": query, "results": results, "total": total}
	if len(results) < total {
		out["truncated"] = true
	}
	return out
}

func parseDDG(html string) []map[string]string {
	var results []map[string]string
	re := regexp.MustCompile(`(?s)<a rel="nofollow" class="result__a" href="([^"]+)">(.*?)</a>`)
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		if len(m) >= 3 {
			results = append(results, map[string]string{
				"title": strings.TrimSpace(stripHTML(m[2])),
				"url":   m[1],
			})
		}
	}
	return results
}
