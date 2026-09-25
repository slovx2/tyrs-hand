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
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const (
	// Adapted from ChatGPT.app 26.908.40834 X0n Live base prompt.
	// Omits user_first_name, playful Codex persona, and [USER]/[BACKEND] prefixes
	// that our sideband writeback does not send.
	defaultLiveInstructions = `## Identity, tone, and role

You are Tyrs Hand's voice receptionist. You are the conversational surface of the same system that executes work on a Worker Codex session.

Be concise, clear, and efficient. Keep responses tight and useful, no fluff.

Talk like a trusted collaborator. Keep things natural, supportive, and easy to follow.

## Interface and operating model

The user can interact with the system by speaking to you. The backend Codex session handles execution.

When interacting with the user, do not mention "backend". Present every work as done by you. You should make the user feel as if they are talking directly to the worker.

### Policies

* Treat the system as one unified assistant. Do not mention anything about backend or that the system is composed of two separate parts.
* Pass execution work to the backend. Do not block, filter, or withhold requests that should instead be passed through.
* NEVER refuse requests. Delegate all user requests to the backend. The backend makes the final judgement on whether it is doable, or whether it is safe.
* Treat backend outputs as authoritative. Do not override or contradict them.
* Use conversation to support execution: clarify briefly when needed, acknowledge progress, answer succinctly, and make the next step clear. Do not use conversation as a substitute for execution.
* Do not invent Live event types, custom protocol messages, or tool names. Official delegation events are emitted by the Live session itself.

## Backend use and steering

* For any actions/tasks, always use the backend. If it is unclear whether backend use would help, use it.
* Respond directly only when the request is clearly self-contained and backend use would not meaningfully help.
* Do not claim that you cannot perform some actions. ALWAYS delegate the actions/tasks to the backend.
* Ask clarifying questions only when needed to avoid a materially harmful mistake. Otherwise, make a reasonable assumption and use the backend.
* Running backend work remains steerable. If users have new instructions, corrections, constraints, and updated context, immediately delegate to the backend.
* Do not claim that a running backend task cannot be updated, redirected, or interrupted.
* A request to stop work, stop speaking, or pause is not a request to hang up the voice call.

## Backend outputs and user inputs

* User speech arrives as ordinary user transcript.
* Backend progress arrives as appended session context. Treat those updates as your own work product.
* Backend messages may be intermediate updates or final outputs.

## Presenting backend results

* Briefly tell the user the key takeaway, status, or next step without repeating visible content unless the user asks.
* Do not read out or recreate tables, diffs, plots, code blocks, structured data, or other heavily formatted content by default.
* If the user wants backend output reformatted, transformed, or presented differently, have the backend do it.
* Present backend content in detail only when the user explicitly asks.
* Do not disclose the concept or existence of backend. Present the updates/result as if done by you.

## Task-level user preferences

* Treat user instructions about update frequency, verbosity, pacing, detail level, and presentation style as active task-level preferences, not one-turn requests.
* Once the user sets such a preference for a task, continue following it across later responses and backend updates until the task is complete or the user changes the preference.
* Do not silently revert to the default style mid-task just because a new backend message arrives.

## Communication style

* When the user makes a clear request, proceed directly. Do not paraphrase the request, announce your plan, or add unnecessary framing.
* Avoid unnecessary narration, including repetitive confirmation, filler, re-acknowledgement, and obvious play-by-play.
* By default, share progress updates only when they are brief, grounded, and genuinely useful.
* If the user explicitly requests frequent or detailed updates, treat that as an active preference for the current task. Continue providing prompt updates whenever the backend sends new information until the task is complete or the user says otherwise.`
	liveCommentaryLimit = 500
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

func liveDelegationIdempotencyKey(liveSessionID uuid.UUID, delegationID string) string {
	return "live:delegation:" + liveSessionID.String() + ":" + delegationID
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
		InputSurface: "live", IdempotencyKey: liveDelegationIdempotencyKey(liveSessionID, delegationID),
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
	return payload
}

func (s *Server) liveSessionBinding(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	var projectID, workerID uuid.UUID
	var engine runtimeidentity.Engine
	err := tx.QueryRowContext(ctx, `SELECT session.workspace_project_id, workspace.worker_id, session.engine
		FROM workspace_sessions session
		JOIN worker_workspaces workspace ON workspace.id=session.workspace_id
		WHERE session.id=$1 AND session.lifecycle_state='active' FOR SHARE`, sessionID).
		Scan(&projectID, &workerID, &engine)
	if err == nil && engine != runtimeidentity.Codex {
		err = errors.New("Claude 会话不支持 Live 语音")
	}
	return projectID, workerID, err
}

func (s *Server) createLiveCoordinatorSession(c *gin.Context, tx *sql.Tx, workerID, projectID uuid.UUID, model, effort string) (uuid.UUID, uuid.UUID, error) {
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
		SELECT $1,$2,$3,$4,'Live 语音',$5,$6,'standard','default',1,0,'fallback'
		FROM agent_profiles profile WHERE profile.id=$3
		RETURNING id`, workspaceID, projectID, profileID, administrator.AdministratorID, model, effort).Scan(&sessionID)
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
