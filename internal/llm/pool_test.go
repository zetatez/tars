package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tars/internal/config"
)

// mockSSE 返回一个简单的 SSE 流式响应（内容 + [DONE]）。
func mockSSE(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	chunk, _ := json.Marshal(openaiChunk{
		Choices: []struct {
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
		}{
			{
				Delta: struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				}{Content: text},
			},
		},
	})
	done := "data: [DONE]\n\n"
	payload := "data: " + string(chunk) + "\n\n"
	w.Write([]byte(payload))
	w.Write([]byte(done))
}

func providerCfg(name, baseURL, model string, priority int) config.LLMProvider {
	return config.LLMProvider{
		Name: name, Type: "openai", BaseURL: baseURL, Model: model, Priority: priority,
	}
}

// 场景1：首个 provider 返回 500，应 failover 到第二个并成功。
func TestPoolFailoverOn5xx(t *testing.T) {
	var badHits atomic.Int32
	var goodHits atomic.Int32

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		mockSSE(t, w, "from-good")
	}))
	defer good.Close()

	p := NewPool(config.LLM{
		LBStrategy: "priority",
		Retry:      config.Retry{MaxAttempts: 2, Backoff: config.Duration{Duration: time.Millisecond}, MaxBackoff: config.Duration{Duration: time.Millisecond}},
		Providers: []config.LLMProvider{
			providerCfg("bad", bad.URL, "m", 1),
			providerCfg("good", good.URL, "m", 2),
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("expected success after failover, got %v", err)
	}
	if res.Text != "from-good" {
		t.Fatalf("expected text from good provider, got %q", res.Text)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Fatalf("bad=%d good=%d, want bad=1 good=1", badHits.Load(), goodHits.Load())
	}
}

// 场景2：同名 provider（如多个 ark），首个失败应尝试下一个同名 provider。
func TestPoolFailoverSameName(t *testing.T) {
	var badHits atomic.Int32
	var goodHits atomic.Int32

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.Error(w, `{"error":"down"}`, http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		mockSSE(t, w, "ok")
	}))
	defer good.Close()

	p := NewPool(config.LLM{
		LBStrategy: "priority",
		Retry:      config.Retry{MaxAttempts: 2, Backoff: config.Duration{Duration: time.Millisecond}, MaxBackoff: config.Duration{Duration: time.Millisecond}},
		Providers: []config.LLMProvider{
			providerCfg("ark", bad.URL, "m", 1),
			providerCfg("ark", good.URL, "m", 1),
		},
	})

	res, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("expected success after same-name failover, got %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("expected ok, got %q", res.Text)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Fatalf("bad=%d good=%d, want bad=1 good=1", badHits.Load(), goodHits.Load())
	}
}

// 场景3：provider 连接失败（拒绝连接 / 网络错误），应快速失败并 failover。
func TestPoolFailoverOnConnError(t *testing.T) {
	// 一个已关闭的 server：连接会被拒绝，立即失败
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	deadURL := dead.URL
	dead.Close()

	var goodHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		mockSSE(t, w, "alive")
	}))
	defer good.Close()

	p := NewPool(config.LLM{
		LBStrategy: "priority",
		Retry:      config.Retry{MaxAttempts: 2, Backoff: config.Duration{Duration: time.Millisecond}, MaxBackoff: config.Duration{Duration: time.Millisecond}},
		Providers: []config.LLMProvider{
			providerCfg("dead", deadURL, "m", 1),
			providerCfg("good", good.URL, "m", 2),
		},
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after conn-error failover, got %v", err)
	}
	if res.Text != "alive" {
		t.Fatalf("expected alive, got %q", res.Text)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("failover too slow: %v", elapsed)
	}
	if goodHits.Load() != 1 {
		t.Fatalf("good hits=%d, want 1", goodHits.Load())
	}
}

// 场景3b：provider 响应头挂起（黑洞），ResponseHeaderTimeout 触发后应 failover。
func TestPoolFailoverOnResponseHeaderHang(t *testing.T) {
	oldDial, oldTLS, oldHeader := dialTimeout, tlsHandshakeTimeout, responseHeaderTimeout
	dialTimeout, tlsHandshakeTimeout, responseHeaderTimeout = 300*time.Millisecond, 300*time.Millisecond, 500*time.Millisecond
	defer func() { dialTimeout, tlsHandshakeTimeout, responseHeaderTimeout = oldDial, oldTLS, oldHeader }()

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer hang.Close()

	var goodHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		mockSSE(t, w, "alive")
	}))
	defer good.Close()

	p := NewPool(config.LLM{
		LBStrategy: "priority",
		Retry:      config.Retry{MaxAttempts: 2, Backoff: config.Duration{Duration: time.Millisecond}, MaxBackoff: config.Duration{Duration: time.Millisecond}},
		Providers: []config.LLMProvider{
			providerCfg("hang", hang.URL, "m", 1),
			providerCfg("good", good.URL, "m", 2),
		},
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := p.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after header-hang failover, got %v", err)
	}
	if res.Text != "alive" {
		t.Fatalf("expected alive, got %q", res.Text)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("failover too slow: %v", elapsed)
	}
	if goodHits.Load() != 1 {
		t.Fatalf("good hits=%d, want 1", goodHits.Load())
	}
}

// 场景4：所有 provider 都失败，返回聚合错误。
func TestPoolAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"down"}`, http.StatusInternalServerError)
	}))
	defer bad.Close()

	p := NewPool(config.LLM{
		LBStrategy: "priority",
		Retry:      config.Retry{MaxAttempts: 2, Backoff: config.Duration{Duration: time.Millisecond}, MaxBackoff: config.Duration{Duration: time.Millisecond}},
		Providers: []config.LLMProvider{
			providerCfg("a", bad.URL, "m", 1),
			providerCfg("b", bad.URL, "m", 1),
		},
	})
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if strings.Contains(err.Error(), "no llm providers configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}
