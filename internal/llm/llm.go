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

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

var ErrContextOverflow = errors.New("context overflow")

type ToolDef struct {
	Type     string  `json:"type"`
	Function FuncDef `json:"function"`
}

type FuncDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Result struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
}

type Client struct {
	baseURL        string
	apiKey         string
	model          string
	fallbackModels []string
	http           *http.Client
	idleTimeout    time.Duration
	maxAttempts    int
	backoff        time.Duration
}

func New(cfg config.LLM) *Client {
	maxAttempts := cfg.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	return &Client{
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:         cfg.APIKey,
		model:          cfg.Model,
		fallbackModels: cfg.FallbackModels,
		http:           &http.Client{},
		idleTimeout:    cfg.IdleTimeout.Duration,
		maxAttempts:    maxAttempts,
		backoff:        cfg.Retry.Backoff.Duration,
	}
}

func (c *Client) StreamChat(ctx context.Context, req ChatRequest) (*Result, error) {
	models := append([]string{c.model}, c.fallbackModels...)
	var lastErr error
	for _, m := range models {
		req.Model = m
		res, err := c.streamOnce(ctx, req)
		if err == nil {
			return res, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) streamOnce(ctx context.Context, req ChatRequest) (*Result, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff):
			}
		}
		res, err := c.doStream(ctx, req)
		if err == nil {
			return res, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) doStream(ctx context.Context, req ChatRequest) (*Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		lower := strings.ToLower(string(b))
		if strings.Contains(lower, "context") || strings.Contains(lower, "length") || strings.Contains(lower, "token") {
			return nil, ErrContextOverflow
		}
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return c.parseStream(ctx, resp.Body)
}

type streamChunk struct {
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

func (c *Client) parseStream(ctx context.Context, body io.Reader) (*Result, error) {
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

	idle := time.NewTimer(c.idleTimeout)
	defer idle.Stop()
	if c.idleTimeout <= 0 {
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
			return c.finish(res, calls), nil
		case line := <-lines:
			if c.handleLine(line, res, calls) {
				return c.finish(res, calls), nil
			}
			if c.idleTimeout > 0 {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(c.idleTimeout)
			}
		case <-idle.C:
			return nil, errors.New("llm stream idle timeout")
		}
	}
}

func (c *Client) handleLine(line string, res *Result, calls map[int]*ToolCall) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "[DONE]" {
		return true
	}
	var chunk streamChunk
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

func (c *Client) finish(res *Result, calls map[int]*ToolCall) *Result {
	for i := 0; i < len(calls); i++ {
		if tc, ok := calls[i]; ok {
			res.ToolCalls = append(res.ToolCalls, *tc)
		}
	}
	return res
}
