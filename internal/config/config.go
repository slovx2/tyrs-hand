package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
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
	CodexHomeRoot                  string
	AttachmentRoot                 string
	CodexBin                       string
	WorkerDataRoot                 string
	RepoCacheMaxBytes              int64
	WorkerID                       string
	WorkerRole                     string
	WorkerImageDigest              string
	WorkerMaxConcurrentJobs        int
	WorkerControlURL               string
	WorkerCredentialFile           string
	WorkerEnrollmentToken          string
	WorkerProtocolVersion          int
	WorkerAPIAllowlist             []netip.Prefix
	WorkerAPITrustedProxies        []netip.Prefix
	LeaseDuration                  time.Duration
	HeartbeatInterval              time.Duration
	ControlTimeout                 time.Duration
	ToolTimeout                    time.Duration
	TurnIdleTimeout                time.Duration
	TurnMaxDuration                time.Duration
	CodexStatusPollInterval        time.Duration
	CodexReconcileMaxAttempts      int
	CodexResultDeliveryMaxAttempts int
	CodexMaxSteersPerTurn          int
	GitHubReplyGateMaxBlocks       int
	EnableDevelopmentContainers    bool
	DevelopmentRuntimeDir          string
	DevelopmentRuntimeHostDir      string
	EnableSSH                      bool
	SSHAgentDir                    string
	SSHAgentHostDir                string
	BrowserMCPURL                  string
	BrowserMCPTokenFile            string
	BrowserFilesRoot               string
	BrowserFilesHostRoot           string
}

func Load() (Config, error) {
	return load(false)
}

// LoadWorker 允许远程 Worker 在没有数据库、Redis 和主密钥的环境中启动。
func LoadWorker() (Config, error) {
	return load(true)
}

func load(workerProcess bool) (Config, error) {
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
		CodexHomeRoot:                  filepath.Clean(v.GetString("codex_home_root")),
		AttachmentRoot:                 filepath.Clean(v.GetString("attachment_root")),
		CodexBin:                       v.GetString("codex_bin"),
		WorkerDataRoot:                 filepath.Clean(v.GetString("worker_data_root")),
		RepoCacheMaxBytes:              v.GetInt64("repo_cache_max_bytes"),
		WorkerID:                       v.GetString("worker_id"),
		WorkerRole:                     strings.TrimSpace(v.GetString("worker_role")),
		WorkerImageDigest:              strings.TrimSpace(v.GetString("worker_image_digest")),
		WorkerMaxConcurrentJobs:        v.GetInt("worker_max_concurrent_jobs"),
		WorkerControlURL:               strings.TrimRight(v.GetString("worker_control_url"), "/"),
		WorkerCredentialFile:           filepath.Clean(v.GetString("worker_credential_file")),
		WorkerEnrollmentToken:          strings.TrimSpace(v.GetString("worker_enrollment_token")),
		WorkerProtocolVersion:          v.GetInt("worker_protocol_version"),
		LeaseDuration:                  v.GetDuration("lease_duration"),
		HeartbeatInterval:              v.GetDuration("heartbeat_interval"),
		ControlTimeout:                 v.GetDuration("control_timeout"),
		ToolTimeout:                    v.GetDuration("tool_timeout"),
		TurnIdleTimeout:                v.GetDuration("turn_idle_timeout"),
		TurnMaxDuration:                v.GetDuration("turn_max_duration"),
		CodexStatusPollInterval:        v.GetDuration("codex_status_poll_interval"),
		CodexReconcileMaxAttempts:      v.GetInt("codex_reconcile_max_attempts"),
		CodexResultDeliveryMaxAttempts: v.GetInt("codex_result_delivery_max_attempts"),
		CodexMaxSteersPerTurn:          v.GetInt("codex_max_steers_per_turn"),
		GitHubReplyGateMaxBlocks:       v.GetInt("github_reply_gate_max_blocks"),
		EnableDevelopmentContainers:    v.GetBool("enable_development_containers"),
		DevelopmentRuntimeDir:          filepath.Clean(v.GetString("development_runtime_dir")),
		DevelopmentRuntimeHostDir:      filepath.Clean(v.GetString("development_runtime_host_dir")),
		EnableSSH:                      v.GetBool("enable_ssh"),
		SSHAgentDir:                    filepath.Clean(v.GetString("ssh_agent_dir")),
		SSHAgentHostDir:                filepath.Clean(v.GetString("ssh_agent_host_dir")),
		BrowserMCPURL:                  strings.TrimSpace(v.GetString("browser_mcp_url")),
		BrowserMCPTokenFile:            filepath.Clean(v.GetString("browser_mcp_token_file")),
		BrowserFilesRoot:               filepath.Clean(v.GetString("browser_files_root")),
		BrowserFilesHostRoot:           filepath.Clean(v.GetString("browser_files_host_root")),
	}
	var err error
	cfg.WorkerAPIAllowlist, err = parseNetworkList(v.GetString("worker_api_ip_allowlist"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 Worker API IP 白名单: %w", err)
	}
	cfg.WorkerAPITrustedProxies, err = parseNetworkList(v.GetString("worker_api_trusted_proxies"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 Worker API 可信代理: %w", err)
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
	var validateErr error
	if workerProcess {
		validateErr = cfg.ValidateWorker()
	} else {
		validateErr = cfg.Validate()
	}
	if validateErr != nil {
		return Config{}, validateErr
	}
	return cfg, nil
}

func (c Config) RemoteWorker() bool { return strings.TrimSpace(c.WorkerControlURL) != "" }

func (c Config) ValidateWorker() error {
	if !c.RemoteWorker() {
		return c.Validate()
	}
	if c.CodexBin == "" || c.WorkerID == "" {
		return errors.New("配置中的 Codex 可执行文件和 Worker ID 不能为空")
	}
	if c.WorkerMaxConcurrentJobs <= 0 {
		return errors.New("worker_max_concurrent_jobs 必须大于零")
	}
	if c.WorkerRole != "all" && c.WorkerRole != "github" && c.WorkerRole != "discord" {
		return errors.New("远程 worker_role 必须是 all、github 或 discord")
	}
	if c.WorkerProtocolVersion != 5 {
		return errors.New("当前 Worker 只支持协议版本 5")
	}
	if c.WorkerCredentialFile == "." || strings.TrimSpace(c.WorkerCredentialFile) == "" {
		return errors.New("远程 Worker 必须配置凭据文件")
	}
	if c.Environment == "production" && !strings.HasPrefix(c.WorkerControlURL, "https://") {
		return errors.New("生产远程 Worker 的 Control URL 必须使用 HTTPS")
	}
	if err := c.validateWorkerCapabilities(); err != nil {
		return err
	}
	return nil
}

func (c Config) validateWorkerCapabilities() error {
	if c.EnableDevelopmentContainers &&
		(c.DevelopmentRuntimeDir == "." || c.DevelopmentRuntimeHostDir == ".") {
		return errors.New("启用开发容器时必须配置环境运行目录和宿主目录")
	}
	if c.EnableSSH && (c.SSHAgentDir == "." || c.SSHAgentHostDir == ".") {
		return errors.New("启用 SSH 时必须配置 Agent 容器目录和宿主目录")
	}
	if c.BrowserMCPURL != "" {
		parsed, err := url.ParseRequestURI(c.BrowserMCPURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("浏览器 MCP URL 必须是有效的绝对 URL")
		}
		if c.BrowserMCPTokenFile == "." || c.BrowserFilesRoot == "." ||
			c.BrowserFilesHostRoot == "." {
			return errors.New("启用浏览器时必须配置 Token 文件和文件交换目录")
		}
	}
	return nil
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
	if c.WorkerRole != "" && c.WorkerRole != "all" && c.WorkerRole != "github" && c.WorkerRole != "discord" {
		return errors.New("worker_role 必须是 all、github 或 discord")
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
	if err := c.validateWorkerCapabilities(); err != nil {
		return err
	}
	if c.Environment == "production" {
		if len(c.MasterKey) != 32 {
			return errors.New("生产环境必须配置主密钥")
		}
		if !c.CookieSecure {
			return errors.New("生产环境必须启用 Secure Cookie")
		}
		if !strings.HasPrefix(c.PublicURL, "https://") {
			return errors.New("生产环境 Public URL 必须使用 HTTPS")
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
	v.SetDefault("codex_home_root", ".local/worker/codex-homes")
	v.SetDefault("attachment_root", ".local/control/attachments")
	v.SetDefault("codex_bin", "codex")
	v.SetDefault("repo_cache_max_bytes", int64(20*1024*1024*1024))
	v.SetDefault("worker_id", defaultWorkerID())
	v.SetDefault("worker_role", "all")
	v.SetDefault("worker_image_digest", "")
	v.SetDefault("worker_max_concurrent_jobs", 6)
	v.SetDefault("worker_control_url", "")
	v.SetDefault("worker_credential_file", ".local/worker/node-credential")
	v.SetDefault("worker_enrollment_token", "")
	v.SetDefault("worker_protocol_version", 5)
	v.SetDefault("development_runtime_dir", ".local/worker/development-runtime")
	v.SetDefault("development_runtime_host_dir", ".local/worker/development-runtime")
	v.SetDefault("enable_ssh", false)
	v.SetDefault("ssh_agent_dir", "/run/tyrs-hand-ssh-agent")
	v.SetDefault("ssh_agent_host_dir", "/opt/tyrs-hand/ssh-agent")
	v.SetDefault("browser_mcp_url", "")
	v.SetDefault("browser_mcp_token_file", "/run/secrets/browser_mcp_token")
	v.SetDefault("browser_files_root", "/run/tyrs-hand-browser-files")
	v.SetDefault("browser_files_host_root", "/opt/tyrs-hand/browser-files")
	v.SetDefault("worker_api_ip_allowlist", "")
	v.SetDefault("worker_api_trusted_proxies", "127.0.0.1/32,::1/128")
	v.SetDefault("lease_duration", "90s")
	v.SetDefault("heartbeat_interval", "20s")
	v.SetDefault("control_timeout", "30s")
	v.SetDefault("tool_timeout", "60s")
	v.SetDefault("turn_idle_timeout", "15m")
	v.SetDefault("turn_max_duration", "90m")
	v.SetDefault("codex_status_poll_interval", "30s")
	v.SetDefault("codex_reconcile_max_attempts", 3)
	v.SetDefault("codex_result_delivery_max_attempts", 5)
	v.SetDefault("codex_max_steers_per_turn", 5)
	v.SetDefault("github_reply_gate_max_blocks", 3)
	v.SetDefault("enable_development_containers", false)
}

func parseNetworkList(value string) ([]netip.Prefix, error) {
	var result []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if address, err := netip.ParseAddr(raw); err == nil {
			bits := 128
			if address.Is4() {
				bits = 32
			}
			result = append(result, netip.PrefixFrom(address.Unmap(), bits))
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("%q 不是有效的 IP 或 CIDR", raw)
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
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
