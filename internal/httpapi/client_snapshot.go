package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type clientSnapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type clientSessionSnapshot struct {
	clientSessionDetail
	Turns struct {
		Items         []clientTurnPageItem `json:"items"`
		HasMoreBefore bool                 `json:"hasMoreBefore"`
		NextCursor    string               `json:"nextCursor"`
	} `json:"turns"`
	SnapshotCursor int64 `json:"snapshotCursor"`
}

func (s *Server) clientGetSessionSnapshot(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	limit := 20
	if raw := c.Query("turnLimit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			badRequest(c, errors.New("turnLimit 无效"))
			return
		}
	}
	if err = s.ensureSessionRunProjections(c.Request.Context(), id); err != nil {
		problem(c, http.StatusInternalServerError, "准备会话快照失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead, ReadOnly: true,
	})
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建会话快照失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := loadClientSessionSnapshot(c.Request.Context(), tx, id, limit)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取会话快照失败", err)
		return
	}
	if err = tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交会话快照失败", err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (s *Server) ensureSessionRunProjections(ctx context.Context, sessionID uuid.UUID) error {
	rows, err := s.db.QueryContext(ctx, `SELECT run.id FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id=run.control_id
		WHERE control.session_id=$1 ORDER BY run.started_at`, sessionID)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.ensureRunProjection(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func loadClientSessionSnapshot(ctx context.Context, query clientSnapshotQueryer,
	sessionID uuid.UUID, limit int,
) (clientSessionSnapshot, error) {
	var snapshot clientSessionSnapshot
	detail, err := loadSnapshotDetail(ctx, query, sessionID)
	if err != nil {
		return snapshot, err
	}
	snapshot.clientSessionDetail = detail
	if err = query.QueryRowContext(ctx,
		`SELECT COALESCE(max(cursor),0) FROM client_updates`).Scan(&snapshot.SnapshotCursor); err != nil {
		return snapshot, err
	}
	anchors, more, err := loadSnapshotAnchors(ctx, query, sessionID, limit)
	if err != nil {
		return snapshot, err
	}
	snapshot.Turns.Items = make([]clientTurnPageItem, 0, len(anchors))
	for _, anchor := range anchors {
		item, itemErr := loadSnapshotTurn(ctx, query, sessionID, anchor)
		if itemErr != nil {
			return snapshot, itemErr
		}
		snapshot.Turns.Items = append(snapshot.Turns.Items, item)
	}
	snapshot.Turns.HasMoreBefore = more
	if more && len(anchors) > 0 {
		snapshot.Turns.NextCursor = encodeTurnCursor(anchors[0].Seq)
	}
	return snapshot, nil
}

func loadSnapshotDetail(ctx context.Context, query clientSnapshotQueryer,
	sessionID uuid.UUID,
) (clientSessionDetail, error) {
	session, err := scanClientSessionSummary(query.QueryRowContext(ctx,
		`SELECT `+clientSessionSummaryColumns+` FROM workspace_sessions session WHERE session.id=$1`, sessionID))
	if err != nil {
		return clientSessionDetail{}, err
	}
	detail := clientSessionDetail{Session: session, Settings: clientSessionSettings{
		AgentProfileID: session.AgentProfileID, Model: session.Model,
		ReasoningEffort: session.ReasoningEffort, ServiceTier: session.ServiceTier,
		CollaborationMode: session.CollaborationMode, SettingsVersion: session.SettingsVersion,
	}}
	run, err := loadSnapshotCurrentRun(ctx, query, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, nil
	}
	if err != nil {
		return clientSessionDetail{}, err
	}
	detail.CurrentRun = run
	return detail, nil
}

func loadSnapshotCurrentRun(ctx context.Context, query clientSnapshotQueryer,
	sessionID uuid.UUID,
) (*clientRunSnapshot, error) {
	var run clientRunSnapshot
	var model, effort, tier, errorCode, errorMessage sql.NullString
	var finished sql.NullTime
	err := query.QueryRowContext(ctx, `SELECT run.id,run.status,
		COALESCE(run.applied_model,run.model),COALESCE(run.applied_reasoning_effort,run.reasoning_effort),
		COALESCE(run.applied_service_tier,run.service_tier),
		COALESCE(run.applied_collaboration_mode,run.collaboration_mode),
		COALESCE(run.applied_settings_revision,run.settings_revision),run.started_at,
		run.finished_at,run.error_code,run.error_message
		FROM codex_turn_runs run JOIN codex_thread_controls control ON control.id=run.control_id
		WHERE control.session_id=$1 ORDER BY run.started_at DESC LIMIT 1`, sessionID).
		Scan(&run.ID, &run.Status, &model, &effort, &tier, &run.ActualSettings.CollaborationMode,
			&run.ActualSettings.SettingsVersion, &run.StartedAt, &finished, &errorCode, &errorMessage)
	if err != nil {
		return nil, err
	}
	run.ActualSettings.Model = nullableString(model)
	run.ActualSettings.ReasoningEffort = nullableString(effort)
	run.ActualSettings.ServiceTier = nullableString(tier)
	if finished.Valid {
		run.FinishedAt = &finished.Time
	}
	run.ErrorCode = nullableString(errorCode)
	run.ErrorMessage = nullableString(errorMessage)
	run.Timeline = make([]clientTimelineEvent, 0)
	run.PendingInteractives, err = loadSnapshotInteractives(ctx, query, run.ID)
	return &run, err
}

func loadSnapshotAnchors(ctx context.Context, query clientSnapshotQueryer,
	sessionID uuid.UUID, limit int,
) ([]turnAnchor, bool, error) {
	var exists bool
	if err := query.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM workspace_sessions WHERE id=$1)`, sessionID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, sql.ErrNoRows
	}
	rows, err := query.QueryContext(ctx, `WITH anchors AS (
		SELECT 'turn' AS kind,conversation_turn_id::text AS id,min(seq) AS anchor_seq
		FROM session_messages WHERE session_id=$1 AND conversation_turn_id IS NOT NULL
		GROUP BY conversation_turn_id
		UNION ALL SELECT 'message',id::text,seq FROM session_messages
		WHERE session_id=$1 AND conversation_turn_id IS NULL)
		SELECT kind,id,anchor_seq FROM anchors ORDER BY anchor_seq DESC LIMIT $2`, sessionID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	anchors := make([]turnAnchor, 0, limit+1)
	for rows.Next() {
		var anchor turnAnchor
		if err = rows.Scan(&anchor.Kind, &anchor.ID, &anchor.Seq); err != nil {
			return nil, false, err
		}
		anchors = append(anchors, anchor)
	}
	more := len(anchors) > limit
	if more {
		anchors = anchors[:limit]
	}
	for left, right := 0, len(anchors)-1; left < right; left, right = left+1, right-1 {
		anchors[left], anchors[right] = anchors[right], anchors[left]
	}
	return anchors, more, rows.Err()
}

func loadSnapshotTurn(ctx context.Context, query clientSnapshotQueryer,
	sessionID uuid.UUID, anchor turnAnchor,
) (clientTurnPageItem, error) {
	item := clientTurnPageItem{Kind: anchor.Kind, ID: anchor.ID, AnchorSeq: anchor.Seq,
		Messages: make([]clientMessage, 0), Runs: make([]clientTurnRun, 0)}
	statement := `SELECT ` + clientMessageColumns +
		` FROM session_messages WHERE session_id=$1 AND conversation_turn_id=$2 ORDER BY seq`
	if anchor.Kind != "turn" {
		statement = `SELECT ` + clientMessageColumns +
			` FROM session_messages WHERE session_id=$1 AND id=$2`
	}
	rows, err := query.QueryContext(ctx, statement, sessionID, anchor.ID)
	if err != nil {
		return item, err
	}
	for rows.Next() {
		message, scanErr := scanClientMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return item, scanErr
		}
		item.Messages = append(item.Messages, message)
	}
	if err = rows.Close(); err != nil {
		return item, err
	}
	if err = loadSnapshotAttachments(ctx, query, item.Messages); err != nil || anchor.Kind != "turn" {
		return item, err
	}
	turnID, err := uuid.Parse(anchor.ID)
	if err != nil {
		return item, err
	}
	item.Runs, err = loadSnapshotRuns(ctx, query, turnID)
	return item, err
}

func loadSnapshotAttachments(ctx context.Context, query clientSnapshotQueryer,
	messages []clientMessage,
) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, 0, len(messages))
	indexes := make(map[uuid.UUID]int, len(messages))
	for index := range messages {
		messages[index].Attachments = make([]clientAttachment, 0)
		ids = append(ids, messages[index].ID.String())
		indexes[messages[index].ID] = index
	}
	rows, err := query.QueryContext(ctx, `SELECT link.message_id,`+clientAttachmentColumns+`
		FROM session_message_attachments link JOIN session_attachments attachment
		ON attachment.id=link.attachment_id WHERE link.message_id=ANY($1::uuid[])
		ORDER BY link.message_id,link.ordinal`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var messageID uuid.UUID
		var attachment clientAttachment
		var attachedSession sql.NullString
		if err = rows.Scan(&messageID, &attachment.ID, &attachedSession, &attachment.Kind,
			&attachment.OriginalFilename, &attachment.MediaType, &attachment.SizeBytes,
			&attachment.SHA256, &attachment.Status, &attachment.CreatedAt); err != nil {
			return err
		}
		if attachedSession.Valid {
			parsed, parseErr := uuid.Parse(attachedSession.String)
			if parseErr != nil {
				return parseErr
			}
			attachment.SessionID = &parsed
		}
		if index, ok := indexes[messageID]; ok {
			messages[index].Attachments = append(messages[index].Attachments, attachment)
		}
	}
	return rows.Err()
}

func loadSnapshotRuns(ctx context.Context, query clientSnapshotQueryer,
	turnID uuid.UUID,
) ([]clientTurnRun, error) {
	rows, err := query.QueryContext(ctx, `SELECT id,attempt,status,
		COALESCE(applied_model,model),COALESCE(applied_reasoning_effort,reasoning_effort),
		COALESCE(applied_service_tier,service_tier),
		COALESCE(applied_collaboration_mode,collaboration_mode),
		COALESCE(applied_settings_revision,settings_revision),started_at,finished_at,
		error_code,error_message FROM codex_turn_runs
		WHERE primary_intent_id=$1 ORDER BY attempt,started_at`, turnID)
	if err != nil {
		return nil, err
	}
	runs := make([]clientTurnRun, 0)
	for rows.Next() {
		var run clientTurnRun
		var model, effort, tier, errorCode, errorMessage sql.NullString
		var finished sql.NullTime
		if err = rows.Scan(&run.ID, &run.Attempt, &run.Status, &model, &effort, &tier,
			&run.ActualSettings.CollaborationMode, &run.ActualSettings.SettingsVersion,
			&run.StartedAt, &finished, &errorCode, &errorMessage); err != nil {
			return nil, err
		}
		run.ActualSettings.Model = nullableString(model)
		run.ActualSettings.ReasoningEffort = nullableString(effort)
		run.ActualSettings.ServiceTier = nullableString(tier)
		if finished.Valid {
			run.FinishedAt = &finished.Time
		}
		run.ErrorCode = nullableString(errorCode)
		run.ErrorMessage = nullableString(errorMessage)
		runs = append(runs, run)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for index := range runs {
		if runs[index].Segments, err = loadSnapshotSegments(ctx, query, runs[index].ID); err != nil {
			return nil, err
		}
		if runs[index].PendingInteractives, err = loadSnapshotInteractives(
			ctx, query, runs[index].ID,
		); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func loadSnapshotSegments(ctx context.Context, query clientSnapshotQueryer,
	runID uuid.UUID,
) ([]clientRunSegment, error) {
	rows, err := query.QueryContext(ctx, `SELECT segment.id,segment.sequence,segment.trigger_type,
		segment.trigger_message_id::text,segment.interactive_request_id::text,
		segment.start_event_sequence,segment.end_event_sequence,count(activity.id)
		FROM run_process_segments segment LEFT JOIN run_process_activities activity
		ON activity.segment_id=segment.id AND activity.kind<>'final_answer'
		WHERE segment.run_id=$1 GROUP BY segment.id ORDER BY segment.sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	segments := make([]clientRunSegment, 0)
	for rows.Next() {
		var segment clientRunSegment
		var message, interactive sql.NullString
		var end sql.NullInt64
		if err = rows.Scan(&segment.ID, &segment.Sequence, &segment.TriggerType, &message,
			&interactive, &segment.StartEventSequence, &end, &segment.ActivityCount); err != nil {
			return nil, err
		}
		if message.Valid {
			value, parseErr := uuid.Parse(message.String)
			if parseErr != nil {
				return nil, parseErr
			}
			segment.TriggerMessageID = &value
		}
		if interactive.Valid {
			value, parseErr := uuid.Parse(interactive.String)
			if parseErr != nil {
				return nil, parseErr
			}
			segment.InteractiveRequestID = &value
		}
		if end.Valid {
			segment.EndEventSequence = &end.Int64
		}
		segments = append(segments, segment)
	}
	return segments, rows.Err()
}

func loadSnapshotInteractives(ctx context.Context, query clientSnapshotQueryer,
	runID uuid.UUID,
) ([]clientInteractiveSnapshot, error) {
	rows, err := query.QueryContext(ctx, `SELECT request.id,request.status,request.questions,
		request.deadline_at FROM codex_interactive_requests request
		JOIN codex_turn_runs run ON run.id=request.run_id
		WHERE request.run_id=$1 AND request.status='pending' AND run.finished_at IS NULL
		AND run.status NOT IN ('completed','failed','canceled') ORDER BY request.created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientInteractiveSnapshot, 0)
	for rows.Next() {
		var item clientInteractiveSnapshot
		var deadline sql.NullTime
		if err = rows.Scan(&item.ID, &item.Status, &item.Questions, &deadline); err != nil {
			return nil, err
		}
		item.Secret = interactiveQuestionsSecret(item.Questions)
		if deadline.Valid {
			item.DeadlineAt = &deadline.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
