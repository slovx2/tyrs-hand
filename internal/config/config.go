package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
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
	AttachmentRoot                 string
	CodexBin                       string
	WorkerDataRoot                 string
	RepoCacheMaxBytes              int64
	WorkerID                       string
	WorkerRole                     string
	WorkerMaxConcurrentJobs        int
	WorkerControlURL               string
	WorkerCredentialFile           string
	WorkerEnrollmentToken          string
	WorkerProtocolVersion          int
	WorkerSSHListenAddr            string
	WorkerSSHHostKeyFile           string
	WorkerAuthorizedKeysFile       string
	WorkerWorkspaceRoot            string
	WorkerCodexHome                string
	WorkerHome                     string
	WorkerShell                    string
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
	EnableSSH                      bool
	SSHAgentDir                    string
	BrowserMCPURL                  string
	BrowserMCPTokenFile            string
	BrowserAgentAddress            string
	BrowserFilesRoot               string
}

func Load() (Config, error) {
	return load(false)
}

// LoadWorker 允许宿主 Worker 在没有数据库、Redis 和主密钥的环境中启动。
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
		AttachmentRoot:                 filepath.Clean(v.GetString("attachment_root")),
		CodexBin:                       v.GetString("codex_bin"),
		WorkerDataRoot:                 filepath.Clean(v.GetString("worker_data_root")),
		RepoCacheMaxBytes:              v.GetInt64("repo_cache_max_bytes"),
		WorkerID:                       v.GetString("worker_id"),
		WorkerRole:                     strings.TrimSpace(v.GetString("worker_role")),
		WorkerMaxConcurrentJobs:        v.GetInt("worker_max_concurrent_jobs"),
		WorkerControlURL:               strings.TrimRight(v.GetString("worker_control_url"), "/"),
		WorkerCredentialFile:           filepath.Clean(v.GetString("worker_credential_file")),
		WorkerEnrollmentToken:          strings.TrimSpace(v.GetString("worker_enrollment_token")),
		WorkerProtocolVersion:          v.GetInt("worker_protocol_version"),
		WorkerSSHListenAddr:            strings.TrimSpace(v.GetString("worker_ssh_listen_addr")),
		WorkerSSHHostKeyFile:           filepath.Clean(v.GetString("worker_ssh_host_key_file")),
		WorkerAuthorizedKeysFile:       filepath.Clean(v.GetString("worker_authorized_keys_file")),
		WorkerWorkspaceRoot:            filepath.Clean(v.GetString("worker_workspace_root")),
		WorkerCodexHome:                filepath.Clean(v.GetString("worker_codex_home")),
		WorkerHome:                     filepath.Clean(v.GetString("worker_home")),
		WorkerShell:                    filepath.Clean(v.GetString("worker_shell")),
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
		EnableSSH:                      v.GetBool("enable_ssh"),
		SSHAgentDir:                    filepath.Clean(v.GetString("ssh_agent_dir")),
		BrowserMCPURL:                  strings.TrimSpace(v.GetString("browser_mcp_url")),
		BrowserMCPTokenFile:            filepath.Clean(v.GetString("browser_mcp_token_file")),
		BrowserAgentAddress:            strings.TrimSpace(v.GetString("browser_agent_address")),
		BrowserFilesRoot:               filepath.Clean(v.GetString("browser_files_root")),
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

func (c Config) ConnectedWorker() bool { return strings.TrimSpace(c.WorkerControlURL) != "" }

func (c Config) ValidateWorker() error {
	if !c.ConnectedWorker() {
		return c.Validate()
	}
	if c.CodexBin == "" || c.WorkerID == "" {
		return errors.New("配置中的 Codex 可执行文件和 Worker ID 不能为空")
	}
	if c.WorkerMaxConcurrentJobs <= 0 {
		return errors.New("worker_max_concurrent_jobs 必须大于零")
	}
	if c.WorkerRole != "all" && c.WorkerRole != "github" && c.WorkerRole != "discord" {
		return errors.New("宿主 Worker 的 worker_role 必须是 all、github 或 discord")
	}
	if c.WorkerProtocolVersion != workerprotocol.Version {
		return fmt.Errorf("当前 Worker 只支持协议版本 %d", workerprotocol.Version)
	}
	if c.WorkerCredentialFile == "." || strings.TrimSpace(c.WorkerCredentialFile) == "" {
		return errors.New("宿主 Worker 必须配置凭据文件")
	}
	if c.WorkerSSHListenAddr == "" || c.WorkerSSHHostKeyFile == "." ||
		c.WorkerAuthorizedKeysFile == "." ||
		c.WorkerWorkspaceRoot == "." || c.WorkerCodexHome == "." || c.WorkerHome == "." ||
		c.WorkerShell == "." {
		return errors.New("宿主 Worker 的 SSH、Home、工作区和 Shell 配置不能为空")
	}
	if c.Environment == "production" && !strings.HasPrefix(c.WorkerControlURL, "https://") {
		return errors.New("生产 Worker 的 Control URL 必须使用 HTTPS")
	}
	if err := c.validateWorkerCapabilities(); err != nil {
		return err
	}
	return nil
}

func (c Config) validateWorkerCapabilities() error {
	if c.EnableSSH && c.SSHAgentDir == "." {
		return errors.New("启用 SSH 时必须配置 Agent 目录")
	}
	if c.BrowserMCPURL != "" {
		parsed, err := url.ParseRequestURI(c.BrowserMCPURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("浏览器 MCP URL 必须是有效的绝对 URL")
		}
		if c.BrowserMCPTokenFile == "." || c.BrowserFilesRoot == "." {
			return errors.New("启用浏览器时必须配置 Token 和文件交换目录")
		}
		if _, _, err := net.SplitHostPort(c.BrowserAgentAddress); err != nil {
			return errors.New("浏览器 Agent 地址必须是 host:port")
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
	if c.CodexStatusPollInterval <= 0 || c.CodexReconcileMaxAttempts <= 0 ||
		c.CodexResultDeliveryMaxAttempts <= 0 || c.CodexMaxSteersPerTurn <= 0 ||
		c.GitHubReplyGateMaxBlocks <= 0 {
		return errors.New("codex 控制层的轮询和尝试次数必须大于零")
	}
	if c.LeaseDuration <= c.HeartbeatInterval*2 {
		return errors.New("lease_duration 必须大于 heartbeat_interval 的两倍")
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
	home, _ := os.UserHomeDir()
	stateRoot := defaultHostWorkerStateRoot(home)
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
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
	v.SetDefault("worker_data_root", stateRoot)
	v.SetDefault("repo_cache_root", filepath.Join(stateRoot, "repo-cache"))
	v.SetDefault("worktree_root", filepath.Join(stateRoot, "worktrees", "github"))
	v.SetDefault("attachment_root", ".local/control/attachments")
	v.SetDefault("codex_bin", "codex")
	v.SetDefault("repo_cache_max_bytes", int64(20*1024*1024*1024))
	v.SetDefault("worker_id", defaultWorkerID())
	v.SetDefault("worker_role", "all")
	v.SetDefault("worker_max_concurrent_jobs", 6)
	v.SetDefault("worker_control_url", "")
	v.SetDefault("worker_credential_file", filepath.Join(stateRoot, "control-state", "worker-credential"))
	v.SetDefault("worker_enrollment_token", "")
	v.SetDefault("worker_protocol_version", workerprotocol.Version)
	v.SetDefault("worker_ssh_listen_addr", ":2222")
	v.SetDefault("worker_ssh_host_key_file", filepath.Join(stateRoot, "ssh", "host_key"))
	v.SetDefault("worker_authorized_keys_file", filepath.Join(stateRoot, "ssh", "authorized_keys"))
	v.SetDefault("worker_workspace_root", filepath.Join(home, "tyrs-hand", "workspaces"))
	v.SetDefault("worker_codex_home", codexHome)
	v.SetDefault("worker_home", home)
	v.SetDefault("worker_shell", shell)
	v.SetDefault("enable_ssh", true)
	v.SetDefault("ssh_agent_dir", filepath.Join(stateRoot, "ssh-agent"))
	v.SetDefault("browser_mcp_url", "")
	v.SetDefault("browser_mcp_token_file", filepath.Join(stateRoot, "browser", "token"))
	v.SetDefault("browser_agent_address", "127.0.0.1:8934")
	v.SetDefault("browser_files_root", filepath.Join(stateRoot, "browser", "files"))
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
}

func defaultHostWorkerStateRoot(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Tyrs Hand", "worker")
	}
	dataRoot := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataRoot, "tyrs-hand", "worker")
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
