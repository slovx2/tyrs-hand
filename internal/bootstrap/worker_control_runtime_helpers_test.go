//go:build integration

package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/httpapi"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type controlRuntimeFixture struct {
	cfg    config.Config
	db     *sql.DB
	signer ssh.Signer
}

func newControlRuntimeFixture(t *testing.T, ctx context.Context, modelURL string) controlRuntimeFixture {
	t.Helper()
	bin, adapter := os.Getenv("TYRS_HAND_TEST_CODEX_BIN"), os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")
	require.NotEmpty(t, bin, "必须使用固定 Codex CLI")
	require.NotEmpty(t, adapter, "必须使用固定适配器及 SDK")
	dsn, redisSocket := os.Getenv("TYRS_HAND_TEST_DATABASE_URL"), os.Getenv("TYRS_HAND_TEST_REDIS_SOCKET")
	require.NotEmpty(t, dsn, "缺少临时 PostgreSQL，不能 skip")
	require.NotEmpty(t, redisSocket, "缺少临时 Redis，不能 skip")
	db, err := database.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, database.Migrate(ctx, db))
	cache := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket})
	t.Cleanup(func() { _ = cache.Close() })
	require.NoError(t, cache.Ping(ctx).Err())
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	controlConfig := config.Config{LeaseDuration: time.Minute, CodexMaxSteersPerTurn: 5, CodexReconcileMaxAttempts: 3}
	control, err := httpapi.NewServer(controlConfig, db, cache, nil, nil, nil,
		platformsettings.NewService(db), nil, nil, secrets.NewStore(db, box), zap.NewNop())
	require.NoError(t, err)
	router := control.Router()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/worker/v1/desktop-thread-requests/") && strings.HasSuffix(r.URL.Path, "/complete") {
			// 稳定覆盖“模型已完成、会话登记仍在网络中”的先后顺序，不能依赖机器快慢。
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	registry := workerregistry.NewService(db)
	registered, enrollment, err := registry.Create(ctx, "protocol-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := registry.Enroll(ctx, enrollment)
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "worker-control-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("HOME", root)
	cfg := config.Config{WorkerMaxConcurrentJobs: 2, WorkerRole: "discord",
		WorkerHome: root, WorkerCodexHome: filepath.Join(root, "codex"),
		WorkerDataRoot: filepath.Join(root, "state"), WorkerWorkspaceRoot: filepath.Join(root, "project"),
		WorkerCredentialFile: filepath.Join(root, "credential"), WorkerAuthorizedKeysFile: filepath.Join(root, "authorized_keys"),
		WorkerSSHHostKeyFile: filepath.Join(root, "host_key"), WorkerSSHListenAddr: "127.0.0.1:0",
		WorkerClaudeSSHListenAddr: "127.0.0.1:0", WorkerClaudeEnabled: true, WorkerClaudeBin: adapter,
		CodexBin: bin, WorkerShell: "/bin/sh", ControlTimeout: 5 * time.Second,
		TurnIdleTimeout: time.Minute, TurnMaxDuration: time.Minute, HeartbeatInterval: time.Second,
		NodeHeartbeatInterval: time.Second, WorkerClaimFallbackInterval: 200 * time.Millisecond,
		WorkerSyncFallbackInterval: time.Second, WorkerControlURL: server.URL, WorkerProtocolVersion: workerprotocol.Version,
		SSHAgentDir: filepath.Join(root, "agent"), WorkerGlobalEnvFile: filepath.Join(root, "codex.env")}
	for _, path := range []string{cfg.WorkerCodexHome, cfg.WorkerWorkspaceRoot, cfg.ClaudeConfigDir()} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	require.NoError(t, os.WriteFile(cfg.WorkerCredentialFile, []byte(credential), 0o600))
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.WorkerAuthorizedKeysFile, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkerCodexHome, "config.toml"), []byte(fmt.Sprintf(`model="mock-model"
model_provider="mock"
approval_policy="never"
[model_providers.mock]
name="Mock"
base_url=%q
wire_api="responses"
supports_websockets=false
request_max_retries=0
stream_max_retries=0
`, modelURL+"/v1")), 0o600))
	settings, err := json.Marshal(map[string]any{"model": "mock-claude", "env": map[string]string{
		"ANTHROPIC_API_KEY": "mock-only", "ANTHROPIC_BASE_URL": modelURL,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ClaudeConfigDir(), "settings.json"), settings, 0o600))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,enabled) VALUES ('protocol',true);
		INSERT INTO discord_members(guild_id,discord_user_id,username) VALUES ('protocol','protocol','owner')`)
	require.NoError(t, err)
	var workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(worker_id,guild_id,owner_discord_user_id)
		VALUES ($1,'protocol','protocol') RETURNING id`, registered.ID).Scan(&workspaceID))
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_projects(workspace_id,relative_path,name,project_kind,
		availability_status,project_source,host_path) VALUES ($1,'workspaces','Workspace','directory','available','workspace_root',$2)`, workspaceID, cfg.WorkerWorkspaceRoot)
	require.NoError(t, err)
	return controlRuntimeFixture{cfg: cfg, db: db, signer: signer}
}

func awaitControlRunCount(t *testing.T, ctx context.Context, db *sql.DB, engine runtimeidentity.Engine, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r JOIN codex_thread_controls c
			ON c.id=r.control_id WHERE c.engine=$1 AND r.status='completed'`, engine).Scan(&count)
		return err == nil && count >= want
	}, 40*time.Second, 100*time.Millisecond, "Control 需要收到 %s 的 %d 个终态", engine, want)
}
