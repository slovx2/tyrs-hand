package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Environment                    string
	HTTPAddr                       string
	SeparateWebhook                bool
	WebhookHTTPAddr                string
	PublicURL                      string
	GitHubAppName                  string
	GitHubAPIURL                   string
	InternalServerURL              string
	DatabaseURL                    string
	RedisURL                       string
	SetupToken                     string
	MasterKey                      []byte
	CookieSecure                   bool
	RepoCacheRoot                  string
	WorktreeRoot                   string
	DiscordWorkspaceRoot           string
	CodexHomeRoot                  string
	CodexBin                       string
	WorkerDataRoot                 string
	RepoCacheMaxBytes              int64
	WorkerID                       string
	WorkerMaxConcurrentJobs        int
	LeaseDuration                  time.Duration
	HeartbeatInterval              time.Duration
	EnvironmentPrepareWaitTimeout  time.Duration
	ControlTimeout                 time.Duration
	ToolTimeout                    time.Duration
	TurnIdleTimeout                time.Duration
	TurnMaxDuration                time.Duration
	CodexStatusPollInterval        time.Duration
	CodexReconcileMaxAttempts      int
	CodexResultDeliveryMaxAttempts int
	CodexMaxSteersPerTurn          int
	GitHubReplyGateMaxBlocks       int
}

func Load() (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("TYRS_HAND")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	setDefaults(v)
	cfg := Config{
		Environment:                    v.GetString("env"),
		HTTPAddr:                       v.GetString("http_addr"),
		SeparateWebhook:                v.GetBool("separate_webhook"),
		WebhookHTTPAddr:                v.GetString("webhook_http_addr"),
		PublicURL:                      strings.TrimRight(v.GetString("public_url"), "/"),
		GitHubAppName:                  v.GetString("github_app_name"),
		GitHubAPIURL:                   strings.TrimRight(v.GetString("github_api_url"), "/"),
		InternalServerURL:              strings.TrimRight(v.GetString("internal_server_url"), "/"),
		DatabaseURL:                    v.GetString("database_url"),
		RedisURL:                       v.GetString("redis_url"),
		SetupToken:                     v.GetString("setup_token"),
		CookieSecure:                   v.GetBool("cookie_secure"),
		RepoCacheRoot:                  filepath.Clean(v.GetString("repo_cache_root")),
		WorktreeRoot:                   filepath.Clean(v.GetString("worktree_root")),
		DiscordWorkspaceRoot:           filepath.Clean(v.GetString("discord_workspace_root")),
		CodexHomeRoot:                  filepath.Clean(v.GetString("codex_home_root")),
		CodexBin:                       v.GetString("codex_bin"),
		WorkerDataRoot:                 filepath.Clean(v.GetString("worker_data_root")),
		RepoCacheMaxBytes:              v.GetInt64("repo_cache_max_bytes"),
		WorkerID:                       v.GetString("worker_id"),
		WorkerMaxConcurrentJobs:        v.GetInt("worker_max_concurrent_jobs"),
		LeaseDuration:                  v.GetDuration("lease_duration"),
		HeartbeatInterval:              v.GetDuration("heartbeat_interval"),
		EnvironmentPrepareWaitTimeout:  v.GetDuration("env_prepare_wait_timeout"),
		ControlTimeout:                 v.GetDuration("control_timeout"),
		ToolTimeout:                    v.GetDuration("tool_timeout"),
		TurnIdleTimeout:                v.GetDuration("turn_idle_timeout"),
		TurnMaxDuration:                v.GetDuration("turn_max_duration"),
		CodexStatusPollInterval:        v.GetDuration("codex_status_poll_interval"),
		CodexReconcileMaxAttempts:      v.GetInt("codex_reconcile_max_attempts"),
		CodexResultDeliveryMaxAttempts: v.GetInt("codex_result_delivery_max_attempts"),
		CodexMaxSteersPerTurn:          v.GetInt("codex_max_steers_per_turn"),
		GitHubReplyGateMaxBlocks:       v.GetInt("github_reply_gate_max_blocks"),
	}
	if strings.TrimSpace(cfg.WorkerID) == "" {
		cfg.WorkerID = defaultWorkerID()
	}

	masterKeyText, err := readSecret(v.GetString("master_key"), v.GetString("master_key_file"))
	if err != nil {
		return Config{}, fmt.Errorf("读取主密钥: %w", err)
	}
	if masterKeyText != "" {
		cfg.MasterKey, err = base64.StdEncoding.DecodeString(masterKeyText)
		if err != nil || len(cfg.MasterKey) != 32 {
			return Config{}, errors.New("环境变量 TYRS_HAND_MASTER_KEY 必须是 32 字节随机值的 base64 编码")
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.HTTPAddr == "" || c.DatabaseURL == "" || c.RedisURL == "" {
		return errors.New("服务的 HTTP、PostgreSQL 和 Redis 配置不能为空")
	}
	if c.SeparateWebhook && (c.WebhookHTTPAddr == "" || c.HTTPAddr == c.WebhookHTTPAddr) {
		return errors.New("开启 Webhook 分离后必须配置不同的监听地址")
	}
	if strings.TrimSpace(c.GitHubAppName) == "" {
		return errors.New("github app 名称不能为空")
	}
	if c.CodexBin == "" || c.WorkerID == "" {
		return errors.New("配置中的 Codex 可执行文件和 Worker ID 不能为空")
	}
	if c.WorkerMaxConcurrentJobs <= 0 {
		return errors.New("worker_max_concurrent_jobs 必须大于零")
	}
	if c.EnvironmentPrepareWaitTimeout <= 0 {
		return errors.New("env_prepare_wait_timeout 必须大于零")
	}
	if c.CodexStatusPollInterval <= 0 || c.CodexReconcileMaxAttempts <= 0 ||
		c.CodexResultDeliveryMaxAttempts <= 0 || c.CodexMaxSteersPerTurn <= 0 ||
		c.GitHubReplyGateMaxBlocks <= 0 {
		return errors.New("codex 控制层的轮询和尝试次数必须大于零")
	}
	if c.LeaseDuration <= c.HeartbeatInterval*2 {
		return errors.New("lease_duration 必须大于 heartbeat_interval 的两倍")
	}
	if c.RepoCacheMaxBytes <= 0 {
		return errors.New("配置的 Repo Cache 容量上限必须大于零")
	}
	if c.Environment == "production" {
		if len(c.MasterKey) != 32 {
			return errors.New("生产环境必须配置主密钥")
		}
		if !c.CookieSecure {
			return errors.New("生产环境必须启用 Secure Cookie")
		}
	}
	return nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("env", "development")
	v.SetDefault("http_addr", ":8080")
	v.SetDefault("separate_webhook", false)
	v.SetDefault("webhook_http_addr", ":8081")
	v.SetDefault("public_url", "http://localhost:8080")
	v.SetDefault("github_app_name", "TyrsHand")
	v.SetDefault("github_api_url", "https://api.github.com")
	v.SetDefault("internal_server_url", "http://localhost:8080")
	v.SetDefault("database_url", "postgres://tyrs_hand:tyrs_hand@localhost:5432/tyrs_hand?sslmode=disable")
	v.SetDefault("redis_url", "redis://localhost:6379/0")
	v.SetDefault("cookie_secure", false)
	v.SetDefault("worker_data_root", ".local/worker")
	v.SetDefault("repo_cache_root", ".local/worker/repo-cache")
	v.SetDefault("worktree_root", ".local/worker/workspaces/github")
	v.SetDefault("discord_workspace_root", ".local/worker/workspaces/discord")
	v.SetDefault("codex_home_root", ".local/worker/codex-homes")
	v.SetDefault("codex_bin", "codex")
	v.SetDefault("repo_cache_max_bytes", int64(20*1024*1024*1024))
	v.SetDefault("worker_id", defaultWorkerID())
	v.SetDefault("worker_max_concurrent_jobs", 6)
	v.SetDefault("lease_duration", "90s")
	v.SetDefault("heartbeat_interval", "20s")
	v.SetDefault("env_prepare_wait_timeout", "10m")
	v.SetDefault("control_timeout", "30s")
	v.SetDefault("tool_timeout", "60s")
	v.SetDefault("turn_idle_timeout", "15m")
	v.SetDefault("turn_max_duration", "90m")
	v.SetDefault("codex_status_poll_interval", "30s")
	v.SetDefault("codex_reconcile_max_attempts", 3)
	v.SetDefault("codex_result_delivery_max_attempts", 5)
	v.SetDefault("codex_max_steers_per_turn", 5)
	v.SetDefault("github_reply_gate_max_blocks", 3)
}

func defaultWorkerID() string {
	hostname, err := os.Hostname()
	if err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "worker-local"
}

func readSecret(value, filename string) (string, error) {
	if value != "" && filename != "" {
		return "", errors.New("主密钥和主密钥文件只能配置一个")
	}
	if filename == "" {
		return strings.TrimSpace(value), nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
