package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"tars/internal/agent"
	"tars/internal/api"
	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/llm"
	"tars/internal/log"
	"tars/internal/mcp"
	"tars/internal/permission"
	"tars/internal/quota"
	"tars/internal/session"
	"tars/internal/store"
	"tars/internal/tools"
)

func main() {
	configPath := flag.String("config", "/opt/tars/config.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	logger, logCloser, err := log.New(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init log: %v\n", err)
		os.Exit(1)
	}
	defer logCloser.Close()
	slog.SetDefault(logger)

	st, err := store.Open(cfg.DataDir, cfg.Storage)
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := auth.EnsureAdmin(st.DB(), cfg.AdminKey); err != nil {
		logger.Error("ensure admin", "err", err)
		os.Exit(1)
	}

	llmPool := llm.NewPool(cfg.LLM)
	reg := tools.Default(cfg)
	permEval := permission.New(cfg)
	ag := agent.New(cfg, st.DB(), llmPool, reg, permEval, logger)

	qc := quota.New(cfg, st.DB())
	mgr := session.NewManager(st.DB(), logger, cfg.DefaultCwd, ag, cfg.DataDir, cfg.Session, qc, cfg.PromptMode)
	ag.SetDelegate(func(ctx context.Context, prompt, cwd, model, keyID, role string, depth int) (string, error) {
		return mgr.RunSync(keyID, cwd, model, role, prompt, depth)
	})
	srv := api.New(cfg, st.DB(), logger, mgr, qc, llmPool)
	mcpSrv := mcp.New(cfg, st.DB(), logger, reg, permEval, mgr)

	root := http.NewServeMux()
	root.Handle("/mcp", mcpSrv.Handler())
	root.Handle("/", srv.Handler())

	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: root,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go st.CheckpointLoop(ctx, cfg.Storage.WALCheckpointInterval.Duration)
	go st.CleanupLoop(ctx, cfg.Storage.Quota, cfg.Session.RetentionDays, logger)
	go mgr.CleanupOrphanedFolders(ctx, cfg.Storage.Quota.ScanInterval.Duration)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("tars starting", "listen", cfg.Listen)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		logger.Error("server error", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Shutdown.Duration)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
	logger.Info("stopped")
}
