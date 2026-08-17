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
			max := sc.Cfg.Tools.Exec.MaxOutput
			return Result{"url": u, "status": resp.StatusCode, "text": truncate(text, max)}, nil
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
			return Result{"query": q, "results": parseDDG(string(b))}, nil
		},
	}
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
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
