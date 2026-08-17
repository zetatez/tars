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

type anthropicProvider struct {
	name        string
	baseURL     string
	apiKey      string
	model       string
	http        *http.Client
	idleTimeout time.Duration
}

func newAnthropicProvider(cfg config.LLMProvider, idleTimeout time.Duration) *anthropicProvider {
	return &anthropicProvider{
		name:        cfg.Name,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		http:        newHTTPClient(),
		idleTimeout: idleTimeout,
	}
}

func (p *anthropicProvider) Name() string { return p.name }

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicChatRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
	Stream      bool               `json:"stream"`
}

func (p *anthropicProvider) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	system, messages := toAnthropicMessages(req.Messages)
	body, err := json.Marshal(anthropicChatRequest{
		Model:       req.Model,
		System:      system,
		Messages:    messages,
		Tools:       toAnthropicTools(req.Tools),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
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
		if resp.StatusCode == http.StatusBadRequest && (strings.Contains(lower, "context") || strings.Contains(lower, "too long")) {
			return nil, ErrContextOverflow
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			return nil, &UnavailableError{Reason: fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))}
		}
		return nil, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return p.parseStream(ctx, resp.Body)
}

func toAnthropicMessages(msgs []Message) (string, []anthropicMessage) {
	system := ""
	var out []anthropicMessage
	var pending []anthropicContent

	for _, m := range msgs {
		switch m.Role {
		case "system":
			system = m.Content
		case "user":
			content := append([]anthropicContent{}, pending...)
			pending = nil
			if m.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: m.Content})
			}
			out = append(out, anthropicMessage{Role: "user", Content: content})
		case "assistant":
			var content []anthropicContent
			if m.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				content = append(content, anthropicContent{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: content})
		case "tool":
			pending = append(pending, anthropicContent{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		}
	}
	if len(pending) > 0 {
		out = append(out, anthropicMessage{Role: "user", Content: pending})
	}
	return system, out
}

func toAnthropicTools(tools []ToolDef) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthropicTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: t.Function.Parameters})
	}
	return out
}

type anthropicChunk struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
}

type anthropicToolAcc struct {
	id    string
	name  string
	input string
}

func (p *anthropicProvider) parseStream(ctx context.Context, body io.Reader) (*Result, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	res := &Result{}
	tools := map[int]*anthropicToolAcc{}

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
			return finishAnthropic(res, tools), nil
		case line := <-lines:
			if p.handleLine(line, res, tools) {
				return finishAnthropic(res, tools), nil
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
			return nil, errors.New("anthropic stream idle timeout")
		}
	}
}

func (p *anthropicProvider) handleLine(line string, res *Result, tools map[int]*anthropicToolAcc) bool {
	if strings.HasPrefix(line, "event:") {
		return false
	}
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	var chunk anthropicChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false
	}
	switch chunk.Type {
	case "content_block_start":
		if chunk.ContentBlock.Type == "tool_use" {
			tools[chunk.Index] = &anthropicToolAcc{id: chunk.ContentBlock.ID, name: chunk.ContentBlock.Name}
		}
	case "content_block_delta":
		switch chunk.Delta.Type {
		case "text_delta":
			res.Text += chunk.Delta.Text
		case "input_json_delta":
			if t, ok := tools[chunk.Index]; ok {
				t.input += chunk.Delta.PartialJSON
			}
		}
	case "message_delta":
		res.FinishReason = mapStopReason(chunk.Delta.StopReason)
	case "message_stop":
		return true
	}
	return false
}

func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return r
	}
}

func finishAnthropic(res *Result, tools map[int]*anthropicToolAcc) *Result {
	for i := 0; i < len(tools); i++ {
		if t, ok := tools[i]; ok {
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:       t.id,
				Type:     "function",
				Function: Function{Name: t.name, Arguments: t.input},
			})
		}
	}
	return res
}
