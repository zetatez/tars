package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Listen        string        `yaml:"listen"`
	DataDir       string        `yaml:"data_dir"`
	DefaultCwd    string        `yaml:"default_cwd"`
	Agent         Agent         `yaml:"agent"`
	LLM           LLM           `yaml:"llm"`
	PromptMode    string        `yaml:"prompt_mode"`
	ReadIsolation bool          `yaml:"read_isolation"`
	SystemProt    SystemProtect `yaml:"system_protect"`
	Quota         Quota         `yaml:"quota"`
	Network       Network       `yaml:"network"`
	Secrets       []string      `yaml:"secrets"`
	Log           Log           `yaml:"log"`
	Session       Session       `yaml:"session"`
	Permissions   Permissions   `yaml:"permissions"`
	Compaction    Compaction    `yaml:"compaction"`
	Memory        Memory        `yaml:"memory"`
	Tools         Tools         `yaml:"tools"`
	ExtTools      []ExtTool     `yaml:"ext_tools"`
	Storage       Storage       `yaml:"storage"`
	Shutdown      Duration      `yaml:"shutdown"`
	Metrics       Metrics       `yaml:"metrics"`
}

type Agent struct {
	SystemPrompt string  `yaml:"system_prompt"`
	Temperature  float64 `yaml:"temperature"`
	MaxTokens    int     `yaml:"max_tokens"`
	MaxToolSteps int     `yaml:"max_tool_steps"`
	Model        string  `yaml:"model"`
}

type LLM struct {
	LBStrategy        string        `yaml:"lb_strategy"`
	StreamIdleTimeout Duration      `yaml:"stream_idle_timeout"`
	Retry             Retry         `yaml:"retry"`
	Providers         []LLMProvider `yaml:"providers"`
}

type LLMProvider struct {
	Name          string `yaml:"name"`
	Type          string `yaml:"type"`
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	Model         string `yaml:"model"`
	ContextWindow int    `yaml:"context_window"`
	Priority      int    `yaml:"priority"`
}

type Retry struct {
	MaxAttempts int      `yaml:"max_attempts"`
	Backoff     Duration `yaml:"backoff"`
	MaxBackoff  Duration `yaml:"max_backoff"`
}

type Tenant struct {
	PerKeyIsolation bool `yaml:"per_key_isolation"`
	ReadIsolation   bool `yaml:"read_isolation"`
}

type SystemProtect struct {
	SystemPaths         []string `yaml:"system_paths"`
	PrivilegedCommands  []string `yaml:"privileged_commands"`
	DestructiveCommands []string `yaml:"destructive_commands"`
	AdminAuto           bool     `yaml:"admin_auto"`
	Backup              FSBackup `yaml:"backup"`
}

type FSBackup struct {
	Enabled bool `yaml:"enabled"`
	Keep    int  `yaml:"keep"`
}

type Quota struct {
	MaxActiveSessions        int `yaml:"max_active_sessions"`
	MaxConcurrentTurnsPerKey int `yaml:"max_concurrent_turns_per_key"`
}

type Network struct {
	WebSearch      bool     `yaml:"websearch"`
	WebFetch       bool     `yaml:"webfetch"`
	ConnectTimeout Duration `yaml:"connect_timeout"`
}

type Log struct {
	Level         string `yaml:"level"`
	Dir           string `yaml:"dir"`
	MaxSizeMB     int    `yaml:"max_size_mb"`
	RetentionDays int    `yaml:"retention_days"`
	JSON          bool   `yaml:"json"`
}

type Session struct {
	RetentionDays int `yaml:"retention_days"`
	LogMaxSizeMB  int `yaml:"log_max_size_mb"`
	LogMaxBackups int `yaml:"log_max_backups"`
}

type Permissions struct {
	Rules    []Rule   `yaml:"rules"`
	Approval Approval `yaml:"approval"`
}

type Rule struct {
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
	Effect   string `yaml:"effect"`
}

type Approval struct {
	Enabled bool     `yaml:"enabled"`
	Timeout Duration `yaml:"timeout"`
}

type Compaction struct {
	ReserveTokens   int `yaml:"reserve_tokens"`
	MinRecentTokens int `yaml:"min_recent_tokens"`
}

type Memory struct {
	Inject   Inject  `yaml:"inject"`
	Extract  Extract `yaml:"extract"`
	Embedder string  `yaml:"embedder"`
}

type Inject struct {
	MaxEntries int `yaml:"max_entries"`
	MaxTokens  int `yaml:"max_tokens"`
}

type Extract struct {
	Enabled          bool     `yaml:"enabled"`
	MaxPerSessionDay int      `yaml:"max_per_session_day"`
	ImportanceCap    int      `yaml:"importance_cap"`
	TTLCap           Duration `yaml:"ttl_cap"`
}

type Tools struct {
	Exec Exec `yaml:"exec"`
}

type Exec struct {
	Timeout   Duration `yaml:"timeout"`
	MaxOutput int      `yaml:"max_output"`
}

type ExtTool struct {
	Name string `yaml:"name"`
	File string `yaml:"file"`
	MCP  struct {
		Server string `yaml:"server"`
		Tool   string `yaml:"tool"`
	} `yaml:"mcp"`
}

type Storage struct {
	Synchronous           string       `yaml:"synchronous"`
	WALCheckpointInterval Duration     `yaml:"wal_checkpoint_interval"`
	BusyTimeout           Duration     `yaml:"busy_timeout"`
	Quota                 StorageQuota `yaml:"quota"`
}

type StorageQuota struct {
	ScanInterval Duration            `yaml:"scan_interval"`
	MinFreeMB    int                 `yaml:"min_free_mb"`
	HardCapMB    int                 `yaml:"hard_cap_mb"`
	Categories   map[string]Category `yaml:"categories"`
}

type Category struct {
	MaxSizeMB     int `yaml:"max_size_mb"`
	RetentionDays int `yaml:"retention_days"`
	Keep          int `yaml:"keep"`
}

type Metrics struct {
	Interval       Duration `yaml:"interval"`
	HistorySeconds int      `yaml:"history_seconds"`
}

func Default() *Config {
	return &Config{
		Listen:     ":8899",
		DataDir:    "/opt/tars/data",
		DefaultCwd: "/opt/tars/work",
		Agent: Agent{
			SystemPrompt: "你是 tars，一个通用 AI agent，可以分析处理任意问题。",
			Temperature:  0.0,
			MaxTokens:    4096,
			MaxToolSteps: 25,
			Model:        "deepseek-chat",
		},
		PromptMode: "interrupt",
		LLM: LLM{
			LBStrategy:        "priority",
			StreamIdleTimeout: Duration{60 * time.Second},
			Retry:             Retry{MaxAttempts: 3, Backoff: Duration{2 * time.Second}, MaxBackoff: Duration{30 * time.Second}},
			Providers: []LLMProvider{
				{
					Name:          "deepseek",
					Type:          "openai",
					BaseURL:       "https://api.deepseek.com/v1",
					Model:         "deepseek-chat",
					ContextWindow: 128000,
					Priority:      1,
				},
			},
		},
		ReadIsolation: false,
		SystemProt: SystemProtect{
			SystemPaths:         []string{"/etc", "/usr", "/boot", "/bin", "/sbin", "/var", "/opt", "/proc", "/sys", "/dev"},
			PrivilegedCommands:  []string{"apt", "apt-get", "yum", "dnf", "pacman", "zypper", "systemctl", "service", "chmod", "chown", "useradd", "usermod", "passwd", "mount", "umount"},
			DestructiveCommands: []string{"mkfs", "fdisk", "parted", "wipefs", "shred", "grub-install", "rm -rf /", "dd of=/dev/"},
			AdminAuto:           true,
			Backup:              FSBackup{Enabled: true, Keep: 5},
		},
		Quota: Quota{
			MaxActiveSessions:        100,
			MaxConcurrentTurnsPerKey: 32,
		},
		Network: Network{WebSearch: true, WebFetch: true, ConnectTimeout: Duration{16 * time.Second}},
		Secrets: []string{},
		Log:     Log{Level: "info", Dir: "", MaxSizeMB: 100, RetentionDays: 30, JSON: true},
		Session: Session{RetentionDays: 30, LogMaxSizeMB: 10, LogMaxBackups: 3},
		Permissions: Permissions{
			Rules:    []Rule{},
			Approval: Approval{Enabled: false, Timeout: Duration{5 * time.Minute}},
		},
		Compaction: Compaction{ReserveTokens: 20000, MinRecentTokens: 8000},
		Memory: Memory{
			Inject:   Inject{MaxEntries: 5, MaxTokens: 1500},
			Extract:  Extract{Enabled: false, MaxPerSessionDay: 50, ImportanceCap: 2, TTLCap: Duration{30 * 24 * time.Hour}},
			Embedder: "none",
		},
		Tools: Tools{
			Exec: Exec{Timeout: Duration{30 * time.Minute}, MaxOutput: 10 * 1024 * 1024},
		},
		Storage: Storage{
			Synchronous:           "full",
			WALCheckpointInterval: Duration{5 * time.Minute},
			BusyTimeout:           Duration{5 * time.Second},
			Quota: StorageQuota{
				ScanInterval: Duration{10 * time.Minute},
				MinFreeMB:    512,
				HardCapMB:    8192,
				Categories: map[string]Category{
					"db":          {MaxSizeMB: 1024},
					"audit":       {MaxSizeMB: 512, RetentionDays: 90},
					"log":         {MaxSizeMB: 1024, RetentionDays: 30},
					"session_log": {MaxSizeMB: 2048, RetentionDays: 30},
					"backup":      {MaxSizeMB: 2048, Keep: 7},
					"tmp":         {MaxSizeMB: 1024},
				},
			},
		},
		Shutdown: Duration{30 * time.Second},
		Metrics:  Metrics{Interval: Duration{5 * time.Second}, HistorySeconds: 600},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := os.ExpandEnv(string(b))
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Log.Dir == "" {
		cfg.Log.Dir = filepath.Join(cfg.DataDir, "logs")
	}
	return cfg, nil
}
