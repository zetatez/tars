package llm

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"sync"
	"time"

	"tars/internal/config"
)

var ErrContextOverflow = errors.New("context overflow")

type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*Result, error)
}

type entry struct {
	provider      Provider
	model         string
	priority      int
	contextWindow int
	healthy       bool
	failCount     int
	cooldownUntil time.Time
}

func (e *entry) isHealthy() bool {
	if e.healthy {
		return true
	}
	return time.Now().After(e.cooldownUntil)
}

type Pool struct {
	entries     []*entry
	strategy    string
	maxAttempts int
	backoff     time.Duration
	maxBackoff  time.Duration
	mu          sync.Mutex
	rrIdx       int
}

func NewPool(cfg config.LLM) *Pool {
	p := &Pool{
		strategy:    cfg.LBStrategy,
		maxAttempts: cfg.Retry.MaxAttempts,
		backoff:     cfg.Retry.Backoff.Duration,
		maxBackoff:  cfg.Retry.MaxBackoff.Duration,
	}
	if p.strategy == "" {
		p.strategy = "priority"
	}
	if p.maxAttempts <= 0 {
		p.maxAttempts = 1
	}
	for _, pc := range cfg.Providers {
		var provider Provider
		switch pc.Type {
		case "anthropic":
			provider = newAnthropicProvider(pc, cfg.StreamIdleTimeout.Duration)
		default:
			provider = newOpenAIProvider(pc, cfg.StreamIdleTimeout.Duration)
		}
		p.entries = append(p.entries, &entry{
			provider:      provider,
			model:         pc.Model,
			priority:      pc.Priority,
			contextWindow: pc.ContextWindow,
			healthy:       true,
		})
	}
	return p
}

func (p *Pool) ContextWindow() int {
	if len(p.entries) == 0 {
		return 128000
	}
	return p.entries[0].contextWindow
}

// Resolve 返回给定 model 对应的展示用 provider 名与生效 model（用于 TUI 展示）。
// model 为空时回退到第一个 provider 的默认 model。
func (p *Pool) Resolve(model string) (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) == 0 {
		return "", model
	}
	if model == "" {
		return p.entries[0].provider.Name(), p.entries[0].model
	}
	for _, e := range p.entries {
		if e.model == model {
			return e.provider.Name(), model
		}
	}
	return p.entries[0].provider.Name(), model
}

func (p *Pool) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	if len(p.entries) == 0 {
		return nil, errors.New("no llm providers configured")
	}
	seen := map[string]bool{}
	var lastErr error
	delay := p.backoff

	for attempt := 0; attempt < p.maxAttempts*len(p.entries); attempt++ {
		e := p.pick(seen)
		if e == nil {
			break
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		res, err := e.provider.Chat(ctx, req)
		if err == nil {
			p.markHealthy(e)
			return res, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, err
		}
		lastErr = err
		var unavail *UnavailableError
		if errors.As(err, &unavail) {
			// provider 不可用（限流/配额/鉴权/5xx）：立即标记并快速 failover，不等待退避
			p.markUnavailable(e)
			continue
		}
		p.markFailure(e)
		delay *= 2
		if p.maxBackoff > 0 && delay > p.maxBackoff {
			delay = p.maxBackoff
		}
	}
	if lastErr == nil {
		lastErr = errors.New("all llm providers failed")
	}
	return nil, lastErr
}

func (p *Pool) pick(seen map[string]bool) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()

	healthy := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		if !seen[e.provider.Name()] && e.isHealthy() {
			healthy = append(healthy, e)
		}
	}
	if len(healthy) == 0 {
		for _, e := range p.entries {
			if !seen[e.provider.Name()] {
				healthy = append(healthy, e)
			}
		}
	}
	if len(healthy) == 0 {
		return nil
	}

	var chosen *entry
	switch p.strategy {
	case "random":
		chosen = healthy[rand.Intn(len(healthy))]
	case "round_robin":
		p.rrIdx = (p.rrIdx) % len(healthy)
		chosen = healthy[p.rrIdx]
		p.rrIdx++
	default: // priority
		sort.SliceStable(healthy, func(i, j int) bool {
			return healthy[i].priority < healthy[j].priority
		})
		chosen = healthy[0]
	}
	if chosen != nil {
		seen[chosen.provider.Name()] = true
	}
	return chosen
}

func (p *Pool) markFailure(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.failCount++
	if e.failCount >= 3 {
		e.healthy = false
		e.cooldownUntil = time.Now().Add(30 * time.Second)
		e.failCount = 0
	}
}

func (p *Pool) markHealthy(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.healthy = true
	e.failCount = 0
}

func (p *Pool) markUnavailable(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.healthy = false
	e.failCount = 0
	e.cooldownUntil = time.Now().Add(30 * time.Second)
}
