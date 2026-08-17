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
	mu          sync.Mutex
	rrIdx       int
}

func NewPool(cfg config.LLM) *Pool {
	p := &Pool{
		strategy:    cfg.Strategy,
		maxAttempts: cfg.Retry.MaxAttempts,
		backoff:     cfg.Retry.Backoff.Duration,
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
			provider = newAnthropicProvider(pc, cfg.IdleTimeout.Duration)
		default:
			provider = newOpenAIProvider(pc, cfg.IdleTimeout.Duration)
		}
		p.entries = append(p.entries, &entry{
			provider:      provider,
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

func (p *Pool) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	if len(p.entries) == 0 {
		return nil, errors.New("no llm providers configured")
	}
	seen := map[string]bool{}
	var lastErr error

	for attempt := 0; attempt < p.maxAttempts*len(p.entries); attempt++ {
		e := p.pick(seen)
		if e == nil {
			break
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(p.backoff):
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
		p.markFailure(e)
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
