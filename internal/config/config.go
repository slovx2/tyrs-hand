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
	Environment       string
	HTTPAddr          string
	PublicURL         string
	GitHubAPIURL      string
	InternalServerURL string
	DatabaseURL       string
	RedisURL          string
	SetupToken        string
	MasterKey         []byte
	CookieSecure      bool
	RepoCacheRoot     string
	WorktreeRoot      string
	CodexHomeRoot     string
	CodexBin          string
	RepoCacheMaxBytes int64
	WorkerID          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	ControlTimeout    time.Duration
	ToolTimeout       time.Duration
	TurnIdleTimeout   time.Duration
	TurnMaxDuration   time.Duration
}

func Load() (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("TYRS_HAND")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	setDefaults(v)
	cfg := Config{
		Environment:       v.GetString("env"),
		HTTPAddr:          v.GetString("http_addr"),
		PublicURL:         strings.TrimRight(v.GetString("public_url"), "/"),
		GitHubAPIURL:      strings.TrimRight(v.GetString("github_api_url"), "/"),
		InternalServerURL: strings.TrimRight(v.GetString("internal_server_url"), "/"),
		DatabaseURL:       v.GetString("database_url"),
		RedisURL:          v.GetString("redis_url"),
		SetupToken:        v.GetString("setup_token"),
		CookieSecure:      v.GetBool("cookie_secure"),
		RepoCacheRoot:     filepath.Clean(v.GetString("repo_cache_root")),
		WorktreeRoot:      filepath.Clean(v.GetString("worktree_root")),
		CodexHomeRoot:     filepath.Clean(v.GetString("codex_home_root")),
		CodexBin:          v.GetString("codex_bin"),
		RepoCacheMaxBytes: v.GetInt64("repo_cache_max_bytes"),
		WorkerID:          v.GetString("worker_id"),
		LeaseDuration:     v.GetDuration("lease_duration"),
		HeartbeatInterval: v.GetDuration("heartbeat_interval"),
		ControlTimeout:    v.GetDuration("control_timeout"),
		ToolTimeout:       v.GetDuration("tool_timeout"),
		TurnIdleTimeout:   v.GetDuration("turn_idle_timeout"),
		TurnMaxDuration:   v.GetDuration("turn_max_duration"),
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
	if c.CodexBin == "" || c.WorkerID == "" {
		return errors.New("配置中的 Codex 可执行文件和 Worker ID 不能为空")
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
	v.SetDefault("public_url", "http://localhost:8080")
	v.SetDefault("github_api_url", "https://api.github.com")
	v.SetDefault("internal_server_url", "http://localhost:8080")
	v.SetDefault("database_url", "postgres://tyrs_hand:tyrs_hand@localhost:5432/tyrs_hand?sslmode=disable")
	v.SetDefault("redis_url", "redis://localhost:6379/0")
	v.SetDefault("cookie_secure", false)
	v.SetDefault("repo_cache_root", ".local/repo-cache")
	v.SetDefault("worktree_root", ".local/worktrees")
	v.SetDefault("codex_home_root", ".local/codex-homes")
	v.SetDefault("codex_bin", "codex")
	v.SetDefault("repo_cache_max_bytes", int64(20*1024*1024*1024))
	v.SetDefault("worker_id", "worker-local")
	v.SetDefault("lease_duration", "90s")
	v.SetDefault("heartbeat_interval", "20s")
	v.SetDefault("control_timeout", "30s")
	v.SetDefault("tool_timeout", "60s")
	v.SetDefault("turn_idle_timeout", "15m")
	v.SetDefault("turn_max_duration", "90m")
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
