package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"tars/internal/a2a"
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
	// 子命令：tars admin-key 打印本机自动派生的 admin key（无需启动服务）
	if len(os.Args) > 1 && os.Args[1] == "admin-key" {
		runAdminKeyCmd(os.Args[2:])
		return
	}

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

	adminKey, err := auth.AdminKeyFromConfig()
	if err != nil {
		logger.Error("admin key", "err", err)
		os.Exit(1)
	}
	if err := auth.EnsureAdmin(st.DB(), adminKey); err != nil {
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
	a2aSrv := a2a.New(cfg, st.DB(), logger, mgr)
	mcpSrv := mcp.New(cfg, st.DB(), logger, reg, permEval, mgr)

	root := http.NewServeMux()
	root.Handle("/mcp", mcpSrv.Handler())
	root.Handle("/a2a", a2aSrv.Handler())
	root.Handle("/.well-known/agent-card.json", a2aSrv.WellKnown())
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

// runAdminKeyCmd 打印派生的 admin key，供管理员部署/重建时查询。
// 用法：tars admin-key [--machine-id M | --server http://host:port]
func runAdminKeyCmd(args []string) {
	machineID := ""
	serverURL := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--machine-id", "-m":
			if i+1 < len(args) {
				machineID = args[i+1]
				i++
			}
		case "--server", "-s":
			if i+1 < len(args) {
				serverURL = args[i+1]
				i++
			}
		}
	}
	if machineID == "" && serverURL != "" {
		// 从远程服务器的无鉴权接口获取 machine-id
		resp, err := http.Get(strings.TrimRight(serverURL, "/") + "/api/v1/machine-id")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: fetch machine-id from %s: %v\n", serverURL, err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		var m struct {
			MachineID string `json:"machine_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || m.MachineID == "" {
			fmt.Fprintf(os.Stderr, "error: fetch machine-id from %s: invalid response\n", serverURL)
			os.Exit(1)
		}
		machineID = m.MachineID
	}
	if machineID == "" {
		machineID = auth.MachineID()
	}
	key := auth.DeriveAdminKey(machineID)
	idx := strings.IndexByte(key, '_')
	fmt.Printf("machine_id : %s\n", machineID)
	fmt.Printf("key_id     : %s\n", key[:idx])
	fmt.Printf("admin_key  : %s\n", key)
	fmt.Println("\n规则：admin_key = <key_id>_<hex(SHA-256^4096(machine_id))>；")
	fmt.Println("secret 经域分离 HMAC + 4096 轮 SHA-256 迭代扩展，不同服务器 machine-id 不同 → key 不同。")
}
