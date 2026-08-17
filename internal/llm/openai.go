package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tars/internal/config"
)

type openaiProvider struct {
	name        string
	baseURL     string
	apiKey      string
	model       string
	http        *http.Client
	idleTimeout time.Duration
}

func newOpenAIProvider(cfg config.LLMProvider, idleTimeout time.Duration) *openaiProvider {
	return &openaiProvider{
		name:        cfg.Name,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		http:        newHTTPClient(),
		idleTimeout: idleTimeout,
	}
}

func (p *openaiProvider) Name() string { return p.name }

type openaiChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

func (p *openaiProvider) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	body, err := json.Marshal(openaiChatRequest{
		Model: req.Model, Messages: req.Messages, Tools: req.Tools,
		Temperature: req.Temperature, MaxTokens: req.MaxTokens, Stream: true,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		// 连接/超时等网络错误：视为 provider 不可用，立即 failover
		return nil, &UnavailableError{Reason: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		lower := strings.ToLower(string(b))
		if resp.StatusCode == http.StatusBadRequest && (strings.Contains(lower, "context") || strings.Contains(lower, "length")) {
			return nil, ErrContextOverflow
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			return nil, &UnavailableError{Reason: fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))}
		}
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return p.parseStream(ctx, resp.Body)
}

type openaiChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *openaiProvider) parseStream(ctx context.Context, body io.Reader) (*Result, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	res := &Result{}
	calls := map[int]*ToolCall{}

	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-streamCtx.Done():
				return
			}
		}
		select {
		case scanErr <- scanner.Err():
		case <-streamCtx.Done():
		}
	}()

	idle := time.NewTimer(p.idleTimeout)
	defer idle.Stop()
	if p.idleTimeout <= 0 {
		idle.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-scanErr:
			if err != nil {
				return nil, err
			}
			return p.finish(res, calls), nil
		case line := <-lines:
			if p.handleLine(line, res, calls) {
				return p.finish(res, calls), nil
			}
			if p.idleTimeout > 0 {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(p.idleTimeout)
			}
		case <-idle.C:
			return nil, errors.New("llm stream idle timeout")
		}
	}
}

func (p *openaiProvider) handleLine(line string, res *Result, calls map[int]*ToolCall) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "[DONE]" {
		return true
	}
	var chunk openaiChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != "" {
			res.Text += ch.Delta.Content
		}
		for _, tc := range ch.Delta.ToolCalls {
			cur, ok := calls[tc.Index]
			if !ok {
				cur = &ToolCall{Type: "function"}
				calls[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
		if ch.FinishReason != nil {
			res.FinishReason = *ch.FinishReason
		}
	}
	return false
}

func (p *openaiProvider) finish(res *Result, calls map[int]*ToolCall) *Result {
	for i := 0; i < len(calls); i++ {
		if tc, ok := calls[i]; ok {
			res.ToolCalls = append(res.ToolCalls, *tc)
		}
	}
	return res
}
