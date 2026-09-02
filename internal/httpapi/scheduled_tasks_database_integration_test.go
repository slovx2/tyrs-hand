//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/scheduledtasks"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerClaimMaterializesScheduledTasksWithoutControlRunCapacity(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "scheduled-claim-worker",
		[]string{"discord"}, 1)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, "", 5*time.Second)
	enrolled, err := client.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client.SetCredential(enrolled.Credential)
	require.NoError(t, client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "integration", ProtocolVersion: workerprotocol.Version,
		SSHHostKeyFingerprint: testWorkerFingerprint(worker.ID),
	}))
	fixture := seedScheduledClaimWorkspace(t, db, worker.ID)
	service := scheduledtasks.NewService(db, 2*time.Second, 5, 3)

	first := createHTTPDueScheduledTask(t, db, service, fixture, "first")
	githubClaim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.Error(t, err, "GitHub Worker 入口保持停用")
	require.Nil(t, githubClaim.Task)
	var runCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
		WHERE scheduled_task_id=$1`, first).Scan(&runCount))
	require.Zero(t, runCount)

	workspaceClaim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, workspaceClaim.Task)
	startWorkerInput(t, ctx, client, workspaceClaim.Task)
	require.Equal(t, "workspace_session", workspaceClaim.Task.Claimed.SourceType)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
		WHERE scheduled_task_id=$1`, first).Scan(&runCount))
	require.Equal(t, 1, runCount)

	second := createHTTPDueScheduledTask(t, db, service, fixture, "second")
	fullClaim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, fullClaim.Task,
		"Control 只交付输入，本地执行槽位由 Worker 协调器决定")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
		WHERE scheduled_task_id=$1`, second).Scan(&runCount))
	require.Equal(t, 1, runCount)

	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls
		SET external_thread_id='scheduled-tool-thread' WHERE id=$1`,
		workspaceClaim.Task.Claimed.ControlID)
	require.NoError(t, err)
	var nextRunBefore time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT next_run_at FROM scheduled_tasks
		WHERE id=$1`, second).Scan(&nextRunBefore))
	namespace := "tyrs_hand"
	arguments, err := json.Marshal(map[string]any{"action": "run_now", "task_id": second})
	require.NoError(t, err)
	result, err := client.CallTool(ctx, workspaceClaim.Task, codex.ToolCallRequest{
		ThreadID: "scheduled-tool-thread", TurnID: "scheduled-tool-turn",
		CallID: "scheduled-tool-call", Namespace: &namespace, Tool: "automation_update",
		Arguments: arguments,
	})
	require.NoError(t, err, "automation 路由不应依赖 GitHub App 配置")
	require.True(t, result.Success)
	result, err = client.CallTool(ctx, workspaceClaim.Task, codex.ToolCallRequest{
		ThreadID: "scheduled-tool-thread", TurnID: "scheduled-tool-turn",
		CallID: "scheduled-tool-call", Namespace: &namespace, Tool: "automation_update",
		Arguments: arguments,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
		WHERE scheduled_task_id=$1`, second).Scan(&runCount))
	require.Equal(t, 1, runCount)
	var nextRunAfter time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT next_run_at FROM scheduled_tasks
		WHERE id=$1`, second).Scan(&nextRunAfter))
	require.Equal(t, nextRunBefore, nextRunAfter)
}

type scheduledClaimFixture struct {
	workspace uuid.UUID
	project   uuid.UUID
	session   uuid.UUID
}

func seedScheduledClaimWorkspace(t *testing.T, db *sql.DB,
	workerID uuid.UUID,
) scheduledClaimFixture {
	t.Helper()
	ctx := context.Background()
	unique := uuid.NewString()
	guildID := "scheduled-http-guild-" + unique
	ownerID := "scheduled-http-owner-" + unique
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,enabled)
		VALUES ($1,true)`, guildID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members(
		guild_id,discord_user_id,username,display_name)
		VALUES ($1,$2,$3,'Scheduled HTTP Owner')`, guildID, ownerID, ownerID)
	require.NoError(t, err)
	var fixture scheduledClaimFixture
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(
		guild_id,owner_discord_user_id,worker_id) VALUES ($1,$2,$3) RETURNING id`,
		guildID, ownerID, workerID).Scan(&fixture.workspace))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects(
		workspace_id,relative_path,name,project_kind,availability_status)
		VALUES ($1,$2,'scheduled-http','directory','available') RETURNING id`,
		fixture.workspace, "projects/"+unique).Scan(&fixture.project))
	var profile uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles
		WHERE name='Default'`).Scan(&profile))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,service_tier)
		VALUES ($1,$2,$3,'Scheduled HTTP','standard') RETURNING id`, fixture.workspace,
		fixture.project, profile).Scan(&fixture.session))
	return fixture
}

func createHTTPDueScheduledTask(t *testing.T, db *sql.DB, service *scheduledtasks.Service,
	fixture scheduledClaimFixture, suffix string,
) uuid.UUID {
	t.Helper()
	name, prompt := "HTTP "+suffix, "执行 "+suffix
	future := time.Now().UTC().Add(24 * time.Hour).Format("20060102T150405Z")
	raw := "DTSTART:" + future + "\nRRULE:FREQ=DAILY"
	task, err := service.Create(context.Background(), scheduledtasks.ToolContext{
		SessionID: fixture.session, ProjectID: fixture.project, CallID: "create-" + suffix,
	}, scheduledtasks.ToolArguments{Kind: scheduledtasks.KindStandalone, Name: &name,
		Prompt: &prompt, Schedule: &raw})
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `UPDATE scheduled_tasks
		SET next_run_at=now()-interval '1 minute' WHERE id=$1`, task.ID)
	require.NoError(t, err)
	return task.ID
}
