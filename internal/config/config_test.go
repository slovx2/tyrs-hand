package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	valid := Config{
		Environment: "development", HTTPAddr: ":8080", WebhookHTTPAddr: ":8081", GitHubAppName: "TyrsHand",
		DatabaseURL: "postgres://db", RedisURL: "redis://cache",
		CodexBin: "codex", WorkerID: "worker", LeaseDuration: 90 * time.Second, HeartbeatInterval: 20 * time.Second,
		RepoCacheMaxBytes: 1024, WorkerMaxConcurrentJobs: 6,
		CodexStatusPollInterval: 30 * time.Second, CodexReconcileMaxAttempts: 3,
		CodexResultDeliveryMaxAttempts: 5, CodexMaxSteersPerTurn: 5, GitHubReplyGateMaxBlocks: 3,
	}
	require.NoError(t, valid.Validate())
	invalid := valid
	invalid.DatabaseURL = ""
	require.Error(t, invalid.Validate())
	invalid = valid
	invalid.LeaseDuration = 30 * time.Second
	require.Error(t, invalid.Validate())
	invalid = valid
	invalid.SeparateWebhook = true
	invalid.WebhookHTTPAddr = invalid.HTTPAddr
	require.Error(t, invalid.Validate())
	production := valid
	production.Environment = "production"
	require.Error(t, production.Validate())
	production.MasterKey = make([]byte, 32)
	production.CookieSecure = true
	production.PublicURL = "http://tyr.example.com"
	require.Error(t, production.Validate())
	production.PublicURL = "https://tyr.example.com"
	require.NoError(t, production.Validate())
}

func TestReadSecretAndLoadMasterKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master-key")
	encoded := base64.StdEncoding.EncodeToString(make([]byte, 32))
	require.NoError(t, os.WriteFile(path, []byte(encoded+"\n"), 0o600))
	value, err := readSecret("", path)
	require.NoError(t, err)
	require.Equal(t, encoded, value)
	_, err = readSecret(encoded, path)
	require.Error(t, err)

	t.Setenv("TYRS_HAND_MASTER_KEY", encoded)
	t.Setenv("TYRS_HAND_DATABASE_URL", "postgres://db")
	t.Setenv("TYRS_HAND_REDIS_URL", "redis://cache")
	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.MasterKey, 32)
	require.False(t, strings.HasSuffix(cfg.PublicURL, "/"))
}

func TestParseWorkerAPINetworkList(t *testing.T) {
	prefixes, err := parseNetworkList("203.0.113.8, 10.20.0.0/16, 2001:db8::1")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.8/32", "10.20.0.0/16", "2001:db8::1/128"},
		[]string{prefixes[0].String(), prefixes[1].String(), prefixes[2].String()})
	_, err = parseNetworkList("not-an-ip")
	require.Error(t, err)
}

func TestValidateAndLoadRemoteWorker(t *testing.T) {
	valid := Config{
		Environment: "production", WorkerControlURL: "https://tyr.example.com",
		CodexBin: "codex", WorkerID: "home-1", WorkerRole: "all",
		WorkerCredentialFile:  "/data/worker/control-state/node-credential",
		WorkerProtocolVersion: 5, WorkerMaxConcurrentJobs: 2,
	}
	require.True(t, valid.RemoteWorker())
	require.NoError(t, valid.ValidateWorker())

	invalid := valid
	invalid.WorkerControlURL = "http://tyr.example.com"
	require.Error(t, invalid.ValidateWorker())
	invalid = valid
	invalid.WorkerRole = "unknown"
	require.Error(t, invalid.ValidateWorker())
	invalid = valid
	invalid.WorkerProtocolVersion = 1
	require.Error(t, invalid.ValidateWorker())
	invalid = valid
	invalid.WorkerCredentialFile = ""
	require.Error(t, invalid.ValidateWorker())
	invalid = valid
	invalid.WorkerMaxConcurrentJobs = 0
	require.Error(t, invalid.ValidateWorker())
	invalid = valid
	invalid.CodexBin = ""
	require.Error(t, invalid.ValidateWorker())

	local := Config{}
	require.False(t, local.RemoteWorker())
	require.Error(t, local.ValidateWorker())

	t.Setenv("TYRS_HAND_ENV", "production")
	t.Setenv("TYRS_HAND_WORKER_CONTROL_URL", "https://tyr.example.com/")
	t.Setenv("TYRS_HAND_WORKER_ID", "home-1")
	t.Setenv("TYRS_HAND_WORKER_ROLE", "github")
	t.Setenv("TYRS_HAND_WORKER_CREDENTIAL_FILE", filepath.Join(t.TempDir(), "credential"))
	t.Setenv("TYRS_HAND_WORKER_PROTOCOL_VERSION", "5")
	t.Setenv("TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS", "2")
	t.Setenv("TYRS_HAND_DATABASE_URL", "")
	t.Setenv("TYRS_HAND_REDIS_URL", "")
	loaded, err := LoadWorker()
	require.NoError(t, err)
	require.Equal(t, "https://tyr.example.com", loaded.WorkerControlURL)
	require.Equal(t, "github", loaded.WorkerRole)
}

func TestDeploymentWorkerProtocolVersion(t *testing.T) {
	version := strconv.Itoa(workerprotocol.Version)
	compose, err := os.ReadFile("../../compose.worker.yaml")
	require.NoError(t, err)
	require.Contains(t, string(compose), `TYRS_HAND_WORKER_PROTOCOL_VERSION: "`+version+`"`)

	example, err := os.ReadFile("../../.env.example")
	require.NoError(t, err)
	require.Contains(t, string(example), "TYRS_HAND_WORKER_PROTOCOL_VERSION="+version)
}

func TestValidateWorkerCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		message string
	}{
		{
			name:    "SSH 缺少 Agent 目录",
			config:  Config{EnableSSH: true, SSHAgentDir: ".", SSHAgentHostDir: "/opt/tyrs-hand/ssh-agent"},
			message: "Agent 容器目录和宿主目录",
		},
		{
			name:    "浏览器 URL 非法",
			config:  Config{BrowserMCPURL: "relative/path"},
			message: "有效的绝对 URL",
		},
		{
			name: "浏览器缺少交换目录",
			config: Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp",
				BrowserMCPTokenFile: "/run/secrets/browser_mcp_token", BrowserFilesRoot: ".",
				BrowserFilesHostRoot: "/opt/tyrs-hand/browser-files"},
			message: "Token 文件和文件交换目录",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, test.config.validateWorkerCapabilities(), test.message)
		})
	}

	valid := Config{
		EnableSSH: true, SSHAgentDir: "/run/tyrs-hand-ssh-agent",
		SSHAgentHostDir:      "/opt/tyrs-hand/ssh-agent",
		BrowserMCPURL:        "http://host.docker.internal:8931/mcp",
		BrowserMCPTokenFile:  "/run/secrets/browser_mcp_token",
		BrowserFilesRoot:     "/run/tyrs-hand-browser-files",
		BrowserFilesHostRoot: "/opt/tyrs-hand/browser-files",
	}
	require.NoError(t, valid.validateWorkerCapabilities())
}
