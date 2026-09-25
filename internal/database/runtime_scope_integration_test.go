//go:build integration

package database

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeScopeBackfillsExistingSessionsAndSchedules(t *testing.T) {
	db := migrationTestDatabase(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version text PRIMARY KEY,checksum char(64) NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`)
	require.NoError(t, err)
	migrations, err := loadMigrations()
	require.NoError(t, err)
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	for _, item := range migrations {
		if strings.HasPrefix(item.version, "030_") {
			break
		}
		require.NoError(t, applyTransactional(ctx, conn, item))
	}
	require.NoError(t, conn.Close())
	_, err = db.ExecContext(ctx, `
		INSERT INTO discord_guilds(guild_id,enabled) VALUES ('legacy',true);
		INSERT INTO discord_members(guild_id,discord_user_id,username) VALUES ('legacy','legacy','legacy');
		WITH worker AS (
			INSERT INTO workers(name) VALUES ('legacy-runtime') RETURNING id
		), workspace AS (
			INSERT INTO worker_workspaces(worker_id,guild_id,owner_discord_user_id)
			SELECT id,'legacy','legacy' FROM worker RETURNING id
		), project AS (
			INSERT INTO workspace_projects(workspace_id,relative_path,name,project_kind,availability_status)
			SELECT id,'workspaces/legacy','legacy','directory','available' FROM workspace RETURNING id,workspace_id
		)
		INSERT INTO workspace_sessions(workspace_id,workspace_project_id,agent_profile_id,title)
		SELECT project.workspace_id,project.id,profile.id,'旧会话' FROM project,agent_profiles profile
		WHERE profile.name='Default';
		INSERT INTO codex_thread_controls(source_type,session_id,agent_profile_id,workspace_id,
			workspace_project_id,worker_id,external_thread_id)
		SELECT 'workspace_session',session.id,session.agent_profile_id,session.workspace_id,
			session.workspace_project_id,workspace.worker_id,'legacy-thread' FROM workspace_sessions session
		JOIN worker_workspaces workspace ON workspace.id=session.workspace_id;
		INSERT INTO desktop_thread_requests(id,workspace_id,operation,request_key,cwd,request_params,
			control_id,external_thread_id,status,workspace_project_id)
		SELECT gen_random_uuid(),workspace_id,'start',repeat('a',64),'/legacy','{}',id,
			external_thread_id,'completed',workspace_project_id FROM codex_thread_controls;
		INSERT INTO scheduled_tasks(workspace_id,workspace_project_id,target_session_id,kind,name,prompt,
			schedule_text,timezone,schedule_kind,interval_seconds)
		SELECT workspace_id,workspace_project_id,id,'heartbeat','旧定时任务','保留这个任务',
			'DTSTART:20300101T000000Z
RRULE:FREQ=HOURLY','UTC','interval',3600 FROM workspace_sessions;
		INSERT INTO codex_turn_intents(control_id,sequence_no,source_type,session_id,workspace_project_id,
			agent_profile_id,idempotency_key,status,input_surface)
		SELECT id,1,'workspace_session',session_id,workspace_project_id,agent_profile_id,
			'desktop-steer:'||workspace_id::text||':'||repeat('b',64),'completed','desktop'
		FROM codex_thread_controls;
		INSERT INTO session_messages(session_id,seq,local_id,message_role,content,turn_intent_id)
		SELECT session_id,1,'desktop:'||idempotency_key,'user','{}',id FROM codex_turn_intents;
		INSERT INTO codex_turn_runs(control_id,primary_intent_id,attempt,worker_id,status)
		SELECT i.control_id,i.id,1,c.worker_id,'completed' FROM codex_turn_intents i
		JOIN codex_thread_controls c ON c.id=i.control_id;
		INSERT INTO tool_calls(run_id,intent_id,thread_id,turn_id,call_id,namespace,tool,arguments,status,result)
		SELECT id,primary_intent_id,'legacy-thread','turn','call','tyrs_hand','automation_update','{}',
			'completed','{"retained":true}' FROM codex_turn_runs;
		INSERT INTO codex_interactive_requests(control_id,run_id,thread_id,turn_id,item_id,
			app_server_generation,app_server_request_id,questions,status,answer)
		SELECT control_id,id,'legacy-thread','turn','item',1,'1','[]','resolved','{"answers":{}}'
		FROM codex_turn_runs;
	`)
	require.NoError(t, err)
	var originalIntent string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id::text FROM codex_turn_intents`).Scan(&originalIntent))
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, Migrate(ctx, db), "迁移重跑不能重复或丢弃旧记录")
	for _, table := range []string{"workspace_sessions", "codex_thread_controls", "desktop_thread_requests", "scheduled_tasks"} {
		var total, codex int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*),count(*) FILTER (WHERE engine='codex') FROM "+table).Scan(&total, &codex))
		require.Equal(t, 1, total, table)
		require.Equal(t, total, codex, table)
	}
	var title, prompt, thread string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.title,task.prompt,control.external_thread_id
		FROM workspace_sessions session JOIN scheduled_tasks task ON task.target_session_id=session.id
		JOIN codex_thread_controls control ON control.session_id=session.id`).Scan(&title, &prompt, &thread))
	require.Equal(t, "旧会话", title)
	require.Equal(t, "保留这个任务", prompt)
	require.Equal(t, "legacy-thread", thread)
	var retainedIntent, status, key, localID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.id::text,intent.status,intent.idempotency_key,message.local_id
		FROM codex_turn_intents intent JOIN session_messages message ON message.turn_intent_id=intent.id`).
		Scan(&retainedIntent, &status, &key, &localID))
	require.Equal(t, originalIntent, retainedIntent)
	require.Equal(t, "completed", status)
	require.Contains(t, key, ":codex:")
	require.Equal(t, "desktop:"+key, localID)
	var retained bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT call.control_id=run.control_id
		AND call.status='completed' AND call.result='{"retained":true}'::jsonb
		AND q.status='resolved' AND q.answer='{"answers":{}}'::jsonb
		FROM tool_calls call JOIN codex_turn_runs run ON run.id=call.run_id
		JOIN codex_interactive_requests q ON q.run_id=run.id`).Scan(&retained))
	require.True(t, retained, "迁移必须保留已确认的工具结果与交互答案")
}
