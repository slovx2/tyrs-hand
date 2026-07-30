package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerPrepareDesktopRollback(c *gin.Context) {
	var request workerprotocol.DesktopRollbackPrepareRequest
	if c.ShouldBindJSON(&request) != nil || request.EnvironmentID == uuid.Nil ||
		!validDesktopRequestKey(request.RequestKey) {
		badRequest(c, errors.New("desktop rollback 参数无效"))
		return
	}
	var params struct {
		ThreadID string `json:"threadId"`
		NumTurns int    `json:"numTurns"`
	}
	if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" || params.NumTurns != 1 {
		badRequest(c, errors.New("desktop rollback 只允许最新一个 turn"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop rollback reservation 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	node := workerNode(c)
	var controlID, conversationID, targetID, profileID uuid.UUID
	var sequence, targetSequence int64
	var controlStatus, targetTurnID, projectionAnchor string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT control.id,
		control.discord_conversation_id, control.next_sequence_no, control.status,
		intent.id, intent.sequence_no, COALESCE(intent.confirmed_codex_turn_id,
			intent.codex_submission_id,''), intent.agent_profile_id,
		COALESCE(intent.projection_anchor,'desktop-' || intent.id::text)
		FROM codex_thread_controls control
		JOIN LATERAL (SELECT * FROM codex_turn_intents candidate
			WHERE candidate.control_id=control.id
				AND candidate.operation IN ('turn_input','replace_last_turn')
			ORDER BY candidate.sequence_no DESC LIMIT 1) intent ON true
		WHERE control.external_thread_id=$1 AND control.development_environment_id=$2
			AND control.execution_node_id=$3 FOR UPDATE OF control, intent`,
		params.ThreadID, request.EnvironmentID, node.ID).Scan(&controlID, &conversationID,
		&sequence, &controlStatus, &targetID, &targetSequence, &targetTurnID, &profileID,
		&projectionAnchor)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Desktop Thread 尚未由 Control 管理", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop rollback 目标失败", err)
		return
	}
	if controlStatus != "idle" || targetTurnID == "" {
		problem(c, http.StatusConflict, "Desktop rollback 目标仍在运行或尚未确认", nil)
		return
	}
	reservationID := uuid.New()
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_turn_intents
		(id,control_id,sequence_no,operation,behavior,resolved_action,target_intent_id,
		source_type,input_surface,discord_conversation_id,repository_id,development_project_id,
		agent_profile_id,idempotency_key,instruction,skills,allowed_tools,dangerous_actions,
		actor_login,actor_permission,reply_policy,reply_status,status,projection_anchor,
		replacement_phase)
		SELECT $1,control_id,$2,'replace_last_turn','start_when_idle','replace',$3,
		source_type,'desktop',discord_conversation_id,repository_id,development_project_id,
		agent_profile_id,$4,'',skills,allowed_tools,dangerous_actions,
		'codex-desktop','owner','silent','skipped','awaiting_confirmation',$5,'reserved'
		FROM codex_turn_intents WHERE id=$3`, reservationID, sequence, targetID,
		"desktop-rollback:"+request.EnvironmentID.String()+":"+request.RequestKey, projectionAnchor)
	if err != nil {
		problem(c, http.StatusConflict, "Desktop rollback 已提交或发生并发冲突", err)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `WITH pending AS (
		SELECT message_id FROM discord_input_messages
		WHERE conversation_id=$1 AND status='received' AND turn_intent_id IS NULL
		ORDER BY received_at DESC, message_id DESC LIMIT 200)
		UPDATE discord_input_messages message SET turn_intent_id=$2,
			replacement_previous_intent_id=NULL FROM pending
		WHERE message.message_id=pending.message_id`, conversationID, reservationID)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_input_messages message
			SET status='skipped',processed_at=now() WHERE message.conversation_id=$1
				AND message.status='received' AND message.turn_intent_id IS NULL
				AND (message.received_at,message.message_id)<(
					SELECT frozen.received_at,frozen.message_id FROM discord_input_messages frozen
					WHERE frozen.turn_intent_id=$2 ORDER BY frozen.received_at,frozen.message_id LIMIT 1
				)`, conversationID, reservationID)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls
			SET next_sequence_no=next_sequence_no+1, updated_at=now() WHERE id=$1`, controlID)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "冻结 Desktop replacement 讨论失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop rollback reservation 失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.DesktopRollbackState{ID: reservationID,
		EnvironmentID: request.EnvironmentID, ThreadID: params.ThreadID, Status: "reserved",
		TargetTurnID: targetTurnID, Params: request.Params})
}

func (s *Server) workerCompleteDesktopRollback(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.DesktopRollbackCompleteRequest
	if c.ShouldBindJSON(&request) != nil || request.EnvironmentID == uuid.Nil {
		badRequest(c, errors.New("desktop rollback complete 参数无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成 Desktop rollback 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	phase, status := "rollback_applied", "awaiting_confirmation"
	if request.Error != "" {
		phase, status = "terminal", "canceled"
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_input_messages
			SET turn_intent_id=replacement_previous_intent_id,
				replacement_previous_intent_id=NULL WHERE turn_intent_id=$1`, requestID)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents intent
			SET replacement_phase=$2,status=$3,replacement_error=NULLIF($4,''),updated_at=now()
			FROM codex_thread_controls control WHERE intent.id=$1
				AND intent.control_id=control.id AND control.development_environment_id=$5`,
			requestID, phase, status, request.Error, request.EnvironmentID)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 Desktop rollback 结果失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop rollback 结果失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerPreflightDesktopTurn(c *gin.Context) {
	var request workerprotocol.DesktopTurnPreflightRequest
	if c.ShouldBindJSON(&request) != nil || request.EnvironmentID == uuid.Nil {
		badRequest(c, errors.New("desktop turn preflight 参数无效"))
		return
	}
	threadID, _, err := desktopTurnInput(request.Params)
	if err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "准备 Desktop replacement 输入失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var reservationID uuid.UUID
	var stillLatest bool
	err = tx.QueryRowContext(c.Request.Context(), `SELECT intent.id,
		intent.sequence_no=(SELECT max(latest.sequence_no) FROM codex_turn_intents latest
			WHERE latest.control_id=control.id
				AND latest.operation IN ('turn_input','replace_last_turn'))
		FROM codex_thread_controls control JOIN codex_turn_intents intent ON intent.control_id=control.id
		WHERE control.external_thread_id=$1 AND control.development_environment_id=$2
			AND intent.operation='replace_last_turn' AND intent.input_surface='desktop'
			AND intent.status='awaiting_confirmation'
			AND intent.replacement_phase IN ('rollback_applied','start_pending')
		ORDER BY intent.sequence_no DESC LIMIT 1 FOR UPDATE OF intent`, threadID,
		request.EnvironmentID).Scan(&reservationID, &stillLatest)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusOK, workerprotocol.DesktopTurnPreflightResponse{Params: request.Params})
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop replacement reservation 失败", err)
		return
	}
	if !stillLatest {
		_, _ = tx.ExecContext(c.Request.Context(), `UPDATE discord_input_messages
			SET turn_intent_id=replacement_previous_intent_id,replacement_previous_intent_id=NULL
			WHERE turn_intent_id=$1`, reservationID)
		_, _ = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents
			SET status='canceled',replacement_phase='terminal',
			replacement_error='replacement 已被更新输入取代',updated_at=now() WHERE id=$1`,
			reservationID)
		if commitErr := tx.Commit(); commitErr != nil {
			problem(c, http.StatusInternalServerError, "取消过期 Desktop replacement 失败", commitErr)
			return
		}
		problem(c, http.StatusConflict, "编辑更早的消息无法触发 Codex rollback；只有当前最新的已提交用户输入可以重跑。", nil)
		return
	}
	discussion, err := desktopReplacementDiscussion(c, tx, reservationID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取冻结讨论失败", err)
		return
	}
	var params map[string]any
	if json.Unmarshal(request.Params, &params) != nil {
		badRequest(c, errors.New("desktop turn params 无效"))
		return
	}
	inputs, _ := params["input"].([]any)
	priorInputs, err := desktopReplacementPriorInputs(c, tx, reservationID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 rollback turn 历史输入失败", err)
		return
	}
	inputs = append(priorInputs, inputs...)
	if discussion != "" {
		inputs = append(inputs, map[string]any{"type": "text", "text": discussion})
	}
	params["input"], params["clientUserMessageId"] = inputs, reservationID.String()
	augmented, err := json.Marshal(params)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents
			SET replacement_phase='start_pending',prepared_input=$2,updated_at=now() WHERE id=$1`,
			reservationID, augmented)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Desktop replacement 输入失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop replacement 输入失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.DesktopTurnPreflightResponse{Params: augmented})
}

func desktopReplacementPriorInputs(c *gin.Context, tx *sql.Tx,
	reservationID uuid.UUID,
) ([]any, error) {
	rows, err := tx.QueryContext(c.Request.Context(), `SELECT prior.instruction
		FROM codex_turn_intents reservation
		JOIN codex_turn_intents target ON target.id=reservation.target_intent_id
		JOIN codex_turn_intents prior ON prior.control_id=target.control_id
			AND prior.confirmed_codex_turn_id=target.confirmed_codex_turn_id
			AND prior.sequence_no<target.sequence_no
			AND prior.operation IN ('turn_input','replace_last_turn')
		WHERE reservation.id=$1 ORDER BY prior.sequence_no`, reservationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []any
	for rows.Next() {
		var instruction string
		if err := rows.Scan(&instruction); err != nil {
			return nil, err
		}
		if strings.TrimSpace(instruction) != "" {
			result = append(result, map[string]any{"type": "text", "text": instruction})
		}
	}
	return result, rows.Err()
}

func desktopReplacementDiscussion(c *gin.Context, tx *sql.Tx, intentID uuid.UUID) (string, error) {
	rows, err := tx.QueryContext(c.Request.Context(), `SELECT message_id,display_name,username,
		body,received_at FROM discord_input_messages WHERE turn_intent_id=$1
		ORDER BY received_at,message_id`, intentID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var result strings.Builder
	for rows.Next() {
		var id, name, username, body string
		var received time.Time
		if err := rows.Scan(&id, &name, &username, &body, &received); err != nil {
			return "", err
		}
		if result.Len() == 0 {
			result.WriteString("以下是 rollback 前已冻结的 Discord 讨论。\n<discord_discussion>\n")
		}
		if strings.TrimSpace(name) == "" {
			name = username
		}
		_, _ = fmt.Fprintf(&result, "  <message id=\"%s\" author=\"%s\" timestamp=\"%s\">\n    %s\n  </message>\n",
			html.EscapeString(id), html.EscapeString(name), received.UTC().Format(time.RFC3339Nano),
			html.EscapeString(body))
	}
	if result.Len() > 0 {
		result.WriteString("</discord_discussion>")
		var attachmentCount int
		if err := tx.QueryRowContext(c.Request.Context(), `SELECT count(*)
			FROM discord_attachments attachment JOIN discord_input_messages message
			ON message.message_id=attachment.message_id WHERE message.turn_intent_id=$1`,
			intentID).Scan(&attachmentCount); err != nil {
			return "", err
		}
		if attachmentCount > 10 {
			_, _ = fmt.Fprintf(&result,
				"\n\n[附件说明：本批次共有 %d 个附件，仅携带时间最新的 10 个。]",
				attachmentCount)
		}
	}
	return result.String(), rows.Err()
}
