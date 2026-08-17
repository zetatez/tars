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

	"tars/internal/api"
	"tars/internal/auth"
	"tars/internal/config"
	"tars/internal/log"
	"tars/internal/session"
	"tars/internal/store"
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

	adminPlain, err := auth.EnsureAdmin(st.DB(), os.Getenv("TARS_ADMIN_KEY"))
	if err != nil {
		logger.Error("ensure admin", "err", err)
		os.Exit(1)
	}
	if adminPlain != "" {
		fmt.Fprintf(os.Stderr, "generated admin key (save it, shown once): %s\n", adminPlain)
		logger.Warn("admin key generated, shown once")
	}

	mgr := session.NewManager(st.DB(), logger, cfg.DefaultCwd)
	srv := api.New(cfg, st.DB(), logger, mgr)

	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go st.CheckpointLoop(ctx, cfg.Storage.WALCheckpointInterval.Duration)

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Shutdown.GracefulTimeout.Duration)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
	logger.Info("stopped")
}
