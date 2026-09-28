//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// MIGRATION-002：GitHub 功能已于 c4398af 停用。双引擎迁移后，真实 Control 与 Worker 在两引擎持续处理会话回合时，
// 迁移遗留的 GitHub 工作项（含被强制指向 Claude 的记录）不被领取、不产生 Run；github 角色领取与旧 Webhook 均被拒绝。
func TestWorkerControlGitHubDisabledRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	var calls sync.Map
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		require.NotContains(t, string(body), "legacy GitHub", "遗留 GitHub 工作项不能进入任何模型请求")
		engine := string(runtimeidentity.Codex)
		if req.URL.Path == "/v1/messages" {
			engine = string(runtimeidentity.Claude)
		}
		// 会话标题任务同样携带用户原文：Codex 不声明工具（TITLE-001），Claude 以 StructuredOutput 生成标题。
		// 只统计显式验收回合。
		var declared struct{ Tools []struct{ Name string } }
		business := strings.Contains(string(body), "MIGRATION_002_") && json.Unmarshal(body, &declared) == nil && len(declared.Tools) > 0
		for _, tool := range declared.Tools {
			business = business && tool.Name != "StructuredOutput"
		}
		if business {
			counter, _ := calls.LoadOrStore(engine, &atomic.Int64{})
			counter.(*atomic.Int64).Add(1)
		}
		bootstrapModelText(w, engine == string(runtimeidentity.Claude))
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)

	// 迁移遗留数据：旧 GitHub 安装、仓库与工作项，分别绑定到本 Worker 的 Codex 与 Claude 控制。
	var installationID, repositoryID, profileID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type) VALUES ('github',9202,'legacy-owner','Organization')
		RETURNING id`).Scan(&installationID))
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1,'github',9202,'legacy-owner','legacy-repo','main','https://example.invalid/legacy.git')
		RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name='Default'`).Scan(&profileID))
	var itemID, legacyIntent uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO work_items
		(repository_id, kind, external_number, title, worker_id) VALUES ($1,'issue',9300,'legacy GitHub',$2)
		RETURNING id`, repositoryID, f.workerID).Scan(&itemID))
	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	legacyIntent, inserted, err := codexcontrol.NewRepository(f.db, time.Minute).Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceGitHub, WorkItemID: itemID, RepositoryID: repositoryID,
		AgentProfileID: profileID, IdempotencyKey: "legacy-github", Instruction: "legacy GitHub instruction",
		ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	// 遗留控制固定属于 Codex；数据库不变量禁止改指 Claude，Claude 入口无从恢复 GitHub。
	var engine string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT c.engine FROM codex_thread_controls c
		JOIN codex_turn_intents i ON i.control_id=c.id WHERE i.id=$1`, legacyIntent).Scan(&engine))
	require.Equal(t, string(runtimeidentity.Codex), engine)
	_, err = f.db.ExecContext(ctx, `UPDATE codex_thread_controls SET engine=$2
		WHERE id=(SELECT control_id FROM codex_turn_intents WHERE id=$1)`, legacyIntent, runtimeidentity.Claude)
	require.ErrorContains(t, err, "会话引擎不能改变")

	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { stopWorker(); <-done; cleanup() }) })

	// 两引擎真实会话回合证明 Worker 领取循环持续工作，遗留 GitHub 工作项在此期间仍不被领取。
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, err := app.Runtimes.Entry(engine)
		require.NoError(t, err)
		client, _, _ := connectBootstrapSSHWithTrace(t, ctx, entry, f.signer, codex.SocketClientOptions{})
		var started struct{ Thread struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "read-only"}, &started))
		events := client.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": started.Thread.ID,
			"input": []map[string]any{{"type": "text", "text": "MIGRATION_002_" + string(engine), "text_elements": []any{}}}}, nil))
		awaitBootstrapTurn(t, ctx, events)
		events.Close()
		awaitControlRunCount(t, ctx, f, engine, 1)
	}
	// 至少再经过数个领取周期。
	time.Sleep(3 * time.Second)
	var status string
	var runs int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT i.status,
		(SELECT count(*) FROM codex_turn_runs r WHERE r.primary_intent_id=i.id)
		FROM codex_turn_intents i WHERE i.id=$1`, legacyIntent).Scan(&status, &runs))
	require.Zero(t, runs, "两引擎都不能领取遗留 GitHub 工作项")
	require.Equal(t, "queued", status, "遗留 GitHub 工作项保持原状态")

	credential, err := os.ReadFile(f.cfg.WorkerCredentialFile)
	require.NoError(t, err)
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		client, err := workerprotocol.NewClient(f.cfg.WorkerControlURL, string(credential), 5*time.Second).ForEngine(engine)
		require.NoError(t, err)
		_, err = client.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
		var gone *workerprotocol.HTTPError
		require.True(t, errors.As(err, &gone), "%s 不能以 github 角色领取: %v", engine, err)
		require.Equal(t, http.StatusGone, gone.StatusCode, "%s 的 github 角色必须明确返回已停用", engine)
	}
	response, err := http.Post(strings.TrimRight(f.cfg.WorkerControlURL, "/")+"/webhooks/github", "application/json",
		strings.NewReader(`{"action":"opened"}`))
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusNotFound, response.StatusCode, "旧 GitHub Webhook 路由必须保持移除")

	for _, engine := range []string{string(runtimeidentity.Codex), string(runtimeidentity.Claude)} {
		counter, ok := calls.Load(engine)
		require.True(t, ok, "%s 必须执行会话回合", engine)
		require.Equal(t, int64(1), counter.(*atomic.Int64).Load(), "%s 只能有一次会话模型请求", engine)
	}
	saveBootstrapArtifact(t, "github-disabled", runtimeidentity.Claude, map[string]any{
		"legacyIntentsClaimed": 0, "githubRoleGone": true, "webhookRoute": "absent"})
}
