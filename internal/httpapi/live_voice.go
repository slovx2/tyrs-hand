package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const (
	defaultLiveInstructions = "你是 Tyrs Hand 的语音接线员。自然对话。用户要求写代码、查进度、改方向、新建或切换 session 时，把工作交给后端，不要自己改仓库，也不要编造事件类型。"
	liveCommentaryLimit     = 500 * 4
)

func liveDelegationID(event map[string]any) string {
	if event == nil {
		return ""
	}
	if value := strings.TrimSpace(fmt.Sprint(event["handoff_id"])); value != "" && value != "<nil>" {
		return value
	}
	if nested, ok := event["delegation"].(map[string]any); ok {
		if value := strings.TrimSpace(fmt.Sprint(nested["id"])); value != "" && value != "<nil>" {
			return value
		}
	}
	if nested, ok := event["item"].(map[string]any); ok {
		if value := strings.TrimSpace(fmt.Sprint(nested["handoff_id"])); value != "" && value != "<nil>" {
			return value
		}
		if value := strings.TrimSpace(fmt.Sprint(nested["id"])); value != "" && value != "<nil>" {
			return value
		}
	}
	if value := strings.TrimSpace(fmt.Sprint(event["id"])); value != "" && value != "<nil>" {
		return value
	}
	return ""
}

func isLiveDelegationEvent(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "session.delegation.created", "delegation.created", "conversation.handoff.requested":
		return true
	default:
		return false
	}
}

func stripLiveVoicePrefix(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{"[STATUS]", "[COMMENTARY]", "[COMPLETE]", "[ANALYSIS]", "[ATTENTION]"} {
		if strings.HasPrefix(trimmed, prefix) {
			return prefix, strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return "", trimmed
}

func truncateLiveCommentary(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= liveCommentaryLimit {
		return text
	}
	runes := []rune(text)
	return string(runes[:liveCommentaryLimit])
}

func (s *Server) enqueueLiveDelegation(ctx context.Context, liveSessionID uuid.UUID, event map[string]any) error {
	delegationID := liveDelegationID(event)
	if delegationID == "" {
		return nil
	}
	text := strings.TrimSpace(s.liveManager.userTranscript(liveSessionID))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID, administratorID uuid.UUID
	var sessionID uuid.NullUUID
	var actorLogin string
	err = tx.QueryRowContext(ctx, `SELECT lc.id, lc.workspace_session_id, lc.administrator_id, admin.username
		FROM live_sessions ls
		JOIN live_conversations lc ON lc.id=ls.conversation_id
		JOIN administrators admin ON admin.id=lc.administrator_id
		WHERE ls.id=$1 FOR UPDATE OF lc`, liveSessionID).
		Scan(&conversationID, &sessionID, &administratorID, &actorLogin)
	if errors.Is(err, sql.ErrNoRows) || !sessionID.Valid {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE live_conversations SET last_delegation_id=$2,updated_at=now()
		WHERE id=$1`, conversationID, delegationID); err != nil {
		return err
	}
	if text == "" {
		return tx.Commit()
	}
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	_, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID.UUID,
		InputSurface: "live", IdempotencyKey: "live:delegation:" + conversationID.String() + ":" + delegationID,
		Instruction: text, Behavior: "steer_if_active", ReplyPolicy: "silent",
		ActorLogin: actorLogin, ActorPermission: "owner", ActorParticipantID: administratorID,
		ActorDisplayName: actorLogin,
	})
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if inserted && s.redis != nil {
		_ = s.redis.Publish(ctx, codexcontrol.WakeupChannel, "queued").Err()
	}
	return nil
}

func (s *Server) appendLiveVoiceCommentary(ctx context.Context, workspaceSessionID uuid.UUID, raw string) {
	if s.liveManager == nil || workspaceSessionID == uuid.Nil {
		return
	}
	channel, text := stripLiveVoicePrefix(raw)
	text = truncateLiveCommentary(text)
	if text == "" {
		return
	}
	var liveSessionID uuid.UUID
	var delegationID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT lc.active_session_id, NULLIF(lc.last_delegation_id,'')
		FROM live_conversations lc
		WHERE lc.workspace_session_id=$1 AND lc.active_session_id IS NOT NULL`,
		workspaceSessionID).Scan(&liveSessionID, &delegationID)
	if err != nil {
		return
	}
	payload := liveVoiceWritebackPayload(text, channel, delegationID.String)
	if err := s.liveManager.writeSidebandJSON(ctx, liveSessionID, payload); err != nil && s.logger != nil {
		s.logger.Warn("Live commentary 写入失败", zap.Error(err))
	}
}

func liveVoiceWritebackPayload(text, channel, delegationID string) map[string]any {
	payload := map[string]any{
		"type": "session.context.append", "event_id": uuid.NewString(),
		"content": []map[string]any{{"type": "input_text", "text": text}},
	}
	if channel != "[ANALYSIS]" {
		payload["channel"] = "speakable"
	}
	if id := strings.TrimSpace(delegationID); id != "" {
		payload["id"] = id
	}
	return payload
}

func (s *Server) liveSessionBinding(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	var projectID, workerID uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT session.workspace_project_id, workspace.worker_id
		FROM workspace_sessions session
		JOIN worker_workspaces workspace ON workspace.id=session.workspace_id
		WHERE session.id=$1 AND session.lifecycle_state='active' FOR SHARE`, sessionID).
		Scan(&projectID, &workerID)
	return projectID, workerID, err
}

func (s *Server) createLiveCoordinatorSession(c *gin.Context, tx *sql.Tx, workerID, projectID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	administrator := c.MustGet("session").(auth.Session)
	var workspaceID, projectWorker uuid.UUID
	err := tx.QueryRowContext(c.Request.Context(), `SELECT project.workspace_id, workspace.worker_id
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		WHERE project.id=$1 AND project.availability_status='available' FOR SHARE`, projectID).
		Scan(&workspaceID, &projectWorker)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if projectWorker != workerID {
		return uuid.Nil, uuid.Nil, errors.New("项目不属于所选 Worker")
	}
	var profileID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT COALESCE(
		(SELECT agent_profile_id FROM client_user_preferences WHERE administrator_id=$1),
		(SELECT id FROM agent_profiles ORDER BY name,id LIMIT 1))`, administrator.AdministratorID).
		Scan(&profileID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var sessionID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,created_by_administrator_id,title,
		model,reasoning_effort,service_tier,collaboration_mode,settings_version,title_revision,title_source)
		SELECT $1,$2,$3,$4,'Live 语音',profile.model,profile.reasoning_effort,'standard','default',1,0,'fallback'
		FROM agent_profiles profile WHERE profile.id=$3
		RETURNING id`, workspaceID, projectID, profileID, administrator.AdministratorID).Scan(&sessionID)
	return sessionID, projectID, err
}

func (s *Server) forwardLiveVoiceText(ctx context.Context, sessionID uuid.UUID, events []workerprotocol.EventInput) {
	for _, event := range events {
		if event.Type != "item/completed" && event.Type != "item/agentMessage/completed" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		item, _ := payload["item"].(map[string]any)
		text := ""
		if item != nil {
			if value, ok := item["text"].(string); ok {
				text = value
			}
		}
		if text == "" {
			if value, ok := payload["text"].(string); ok {
				text = value
			}
		}
		channel, _ := stripLiveVoicePrefix(text)
		if channel == "" {
			continue
		}
		s.appendLiveVoiceCommentary(ctx, sessionID, text)
	}
}
