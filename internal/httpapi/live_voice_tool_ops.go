package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func (s *Server) liveVoiceListSessions(ctx context.Context, workerID uuid.UUID) (codex.ToolCallResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session.id::text, session.title, session.lifecycle_state,
		session.workspace_project_id::text,
		EXISTS(SELECT 1 FROM codex_thread_controls control JOIN codex_turn_runs run ON run.control_id=control.id
			WHERE control.session_id=session.id AND run.status IN ('starting','running','reconciling'))
		FROM workspace_sessions session
		JOIN worker_workspaces workspace ON workspace.id=session.workspace_id
		WHERE workspace.worker_id=$1 AND session.lifecycle_state='active'
		ORDER BY session.last_activity_at DESC LIMIT 50`, workerID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, title, lifecycle, projectID string
		var running bool
		if err := rows.Scan(&id, &title, &lifecycle, &projectID, &running); err != nil {
			return codex.ToolCallResult{}, err
		}
		items = append(items, map[string]any{"sessionId": id, "title": title,
			"lifecycle": lifecycle, "projectId": projectID, "running": running})
	}
	body, _ := json.Marshal(map[string]any{"sessions": items})
	return codex.TextToolResult(string(body), true), nil
}

func (s *Server) liveVoiceCreateSession(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	liveID, workerID uuid.UUID, args map[string]any,
) (codex.ToolCallResult, error) {
	projectID := claimed.ProjectID
	if raw, _ := args["projectId"].(string); strings.TrimSpace(raw) != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return codex.TextToolResult("projectId 无效", false), nil
		}
		projectID = parsed
	}
	var projectWorker, workspaceID uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT workspace.worker_id, project.workspace_id
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		WHERE project.id=$1 AND project.availability_status='available'`, projectID).
		Scan(&projectWorker, &workspaceID)
	if err != nil {
		return codex.TextToolResult("项目不可用", false), nil
	}
	if projectWorker != workerID {
		return codex.TextToolResult("不能在其他 Worker 上创建 session", false), nil
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Live 任务"
	}
	var sessionID uuid.UUID
	err = s.db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,created_by_administrator_id,title,
		service_tier,collaboration_mode,settings_version,title_revision,title_source)
		SELECT $1,$2,$3,(SELECT administrator_id FROM live_conversations WHERE id=$4),
		$5,'standard','default',1,0,'fallback'
		RETURNING id`, workspaceID, projectID, claimed.AgentProfileID, liveID, title).Scan(&sessionID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	body, _ := json.Marshal(map[string]any{"sessionId": sessionID.String(), "title": title})
	return codex.TextToolResult(string(body), true), nil
}

func (s *Server) liveVoiceSendMessage(ctx context.Context, liveID, workerID uuid.UUID, args map[string]any,
) (codex.ToolCallResult, error) {
	sessionID, err := parseLiveToolSessionID(args)
	if err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	text, _ := args["text"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return codex.TextToolResult("text 不能为空", false), nil
	}
	if err := s.ensureSessionOnWorker(ctx, sessionID, workerID); err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	var actorLogin string
	var actorID uuid.UUID
	err = s.db.QueryRowContext(ctx, `SELECT admin.username, lc.administrator_id
		FROM live_conversations lc JOIN administrators admin ON admin.id=lc.administrator_id
		WHERE lc.id=$1`, liveID).Scan(&actorLogin, &actorID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	_, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
		InputSurface: "live", IdempotencyKey: "live:send:" + sessionID.String() + ":" + uuid.NewString(),
		Instruction: text, Behavior: "steer_if_active", ReplyPolicy: "silent",
		ActorLogin: actorLogin, ActorPermission: "owner",
		ActorParticipantID: actorID, ActorDisplayName: actorLogin,
	})
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return codex.ToolCallResult{}, err
	}
	if inserted && s.redis != nil {
		_ = s.redis.Publish(ctx, codexcontrol.WakeupChannel, "queued").Err()
	}
	return codex.TextToolResult(`{"queued":true}`, true), nil
}

func (s *Server) liveVoiceReadSession(ctx context.Context, workerID uuid.UUID, args map[string]any) (codex.ToolCallResult, error) {
	sessionID, err := parseLiveToolSessionID(args)
	if err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	if err := s.ensureSessionOnWorker(ctx, sessionID, workerID); err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	var title, lifecycle string
	var running bool
	err = s.db.QueryRowContext(ctx, `SELECT session.title, session.lifecycle_state,
		EXISTS(SELECT 1 FROM codex_thread_controls control JOIN codex_turn_runs run ON run.control_id=control.id
			WHERE control.session_id=session.id AND run.status IN ('starting','running','reconciling'))
		FROM workspace_sessions session WHERE session.id=$1`, sessionID).
		Scan(&title, &lifecycle, &running)
	if err != nil {
		return codex.TextToolResult("session 不存在", false), nil
	}
	body, _ := json.Marshal(map[string]any{"sessionId": sessionID.String(), "title": title,
		"lifecycle": lifecycle, "running": running})
	return codex.TextToolResult(string(body), true), nil
}

func (s *Server) liveVoiceTransfer(ctx context.Context, liveID, currentSession, workerID uuid.UUID,
	args map[string]any,
) (codex.ToolCallResult, error) {
	sessionID, err := parseLiveToolSessionID(args)
	if err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	if err := s.ensureSessionOnWorker(ctx, sessionID, workerID); err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE live_conversations
		SET previous_workspace_session_id=workspace_session_id,
			workspace_session_id=$2,
			project_id=(SELECT workspace_project_id FROM workspace_sessions WHERE id=$2),
			updated_at=now()
		WHERE id=$1 AND worker_id=$3`, liveID, sessionID, workerID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	updated, _ := result.RowsAffected()
	if updated == 0 {
		return codex.TextToolResult("不能操作其他 Worker 的 session", false), nil
	}
	body, _ := json.Marshal(map[string]any{"transferred": true, "sessionId": sessionID.String()})
	return codex.TextToolResult(string(body), true), nil
}

func (s *Server) liveVoiceEnd(ctx context.Context, liveID uuid.UUID) (codex.ToolCallResult, error) {
	var liveSessionID uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT active_session_id FROM live_conversations
		WHERE id=$1 AND active_session_id IS NOT NULL`, liveID).Scan(&liveSessionID)
	if err != nil {
		return codex.TextToolResult("没有活动的语音会话", false), nil
	}
	_ = s.liveManager.sendClose(ctx, liveSessionID)
	return codex.TextToolResult(`{"ended":true}`, true), nil
}

func parseLiveToolSessionID(args map[string]any) (uuid.UUID, error) {
	raw, _ := args["sessionId"].(string)
	if strings.TrimSpace(raw) == "" {
		raw, _ = args["threadId"].(string)
	}
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, errors.New("sessionId 无效")
	}
	return parsed, nil
}

func (s *Server) ensureSessionOnWorker(ctx context.Context, sessionID, workerID uuid.UUID) error {
	var found uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT workspace.worker_id
		FROM workspace_sessions session
		JOIN worker_workspaces workspace ON workspace.id=session.workspace_id
		WHERE session.id=$1`, sessionID).Scan(&found)
	if err != nil {
		return errors.New("session 不在当前 Worker")
	}
	if found != workerID {
		return errors.New("不能操作其他 Worker 的 session")
	}
	return nil
}
