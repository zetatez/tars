package tools

import (
	"context"
	"database/sql"
	"sync"

	"tars/internal/config"
	"tars/internal/llm"
)

type DelegateFunc func(ctx context.Context, prompt, cwd, model, keyID, role string) (string, error)

type Scope struct {
	Cwd           string
	KeyID         string
	Role          string
	SessionID     string
	DB            *sql.DB
	Cfg           *config.Config
	Delegate      DelegateFunc
	CurrentTokens int
	ContextWindow int
}

type Result = map[string]any

type Tool struct {
	Name         string
	Description  string
	Params       map[string]any
	PolicyAction string
	ResourceKey  string // args 中资源字段名（"path"/"argv"/...），空表示无资源
	ParallelSafe bool
	Execute      func(ctx context.Context, args map[string]any, sc *Scope) (Result, error)
}

type Registry struct {
	tools map[string]*Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]*Tool{}}
}

func (r *Registry) Register(t *Tool) {
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

func (r *Registry) ToLLMTools() []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(r.order))
	for _, n := range r.order {
		t := r.tools[n]
		out = append(out, llm.ToolDef{
			Type: "function",
			Function: llm.FuncDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Params,
			},
		})
	}
	return out
}

type Call struct {
	ID   string
	Name string
	Args map[string]any
}

type CallResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   any    `json:"args"`
	Result Result `json:"result"`
	Status string `json:"status"`
}

func (r *Registry) ExecuteBatch(ctx context.Context, calls []Call, sc *Scope) []CallResult {
	results := make([]CallResult, len(calls))
	i := 0
	for i < len(calls) {
		t, _ := r.Get(calls[i].Name)
		if t == nil {
			results[i] = CallResult{ID: calls[i].ID, Name: calls[i].Name, Args: calls[i].Args, Status: "error", Result: Result{"error": "unknown tool: " + calls[i].Name}}
			i++
			continue
		}
		if !t.ParallelSafe {
			results[i] = r.execOne(ctx, calls[i], t, sc)
			i++
			continue
		}
		j := i
		for j < len(calls) {
			tt, _ := r.Get(calls[j].Name)
			if tt == nil || !tt.ParallelSafe {
				break
			}
			j++
		}
		r.execParallel(ctx, calls[i:j], results[i:j], sc)
		i = j
	}
	return results
}

func (r *Registry) execOne(ctx context.Context, call Call, t *Tool, sc *Scope) CallResult {
	res, err := t.Execute(ctx, call.Args, sc)
	cr := CallResult{ID: call.ID, Name: call.Name, Args: call.Args, Result: res, Status: "ok"}
	if err != nil {
		cr.Status = "error"
		if cr.Result == nil {
			cr.Result = Result{}
		}
		cr.Result["error"] = err.Error()
	}
	return cr
}

func (r *Registry) execParallel(ctx context.Context, calls []Call, results []CallResult, sc *Scope) {
	var wg sync.WaitGroup
	for k := range calls {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			t, _ := r.Get(calls[k].Name)
			results[k] = r.execOne(ctx, calls[k], t, sc)
		}(k)
	}
	wg.Wait()
}
