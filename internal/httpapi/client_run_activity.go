package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientRunActivity struct {
	ID                 uuid.UUID       `json:"id"`
	ItemID             string          `json:"itemId"`
	Kind               string          `json:"kind"`
	FirstEventSequence int64           `json:"firstEventSequence"`
	LastEventSequence  int64           `json:"lastEventSequence"`
	Status             string          `json:"status"`
	Payload            json.RawMessage `json:"payload"`
	OccurredAt         string          `json:"occurredAt"`
}

func (s *Server) loadClientTurnRuns(c *gin.Context, turnID uuid.UUID) ([]clientTurnRun, error) {
	rows, err := s.db.QueryContext(c, `SELECT id,attempt,status,
		COALESCE(applied_model,model),COALESCE(applied_reasoning_effort,reasoning_effort),
		COALESCE(applied_service_tier,service_tier),
		COALESCE(applied_collaboration_mode,collaboration_mode),
		COALESCE(applied_settings_revision,settings_revision),started_at,finished_at,
		error_code,error_message
		FROM codex_turn_runs WHERE primary_intent_id=$1 ORDER BY attempt,started_at`, turnID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
		if err = s.ensureRunProjection(c, run.ID); err != nil {
			return nil, err
		}
		if run.Segments, err = s.loadClientRunSegments(c, run.ID); err != nil {
			return nil, err
		}
		if run.PendingInteractives, err = s.loadPendingInteractives(c, run.ID); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Server) loadClientRunSegments(c *gin.Context, runID uuid.UUID) ([]clientRunSegment, error) {
	rows, err := s.db.QueryContext(c, `SELECT segment.id,segment.sequence,segment.trigger_type,
		segment.trigger_message_id::text,segment.interactive_request_id::text,
		segment.start_event_sequence,segment.end_event_sequence,count(activity.id)
		FROM run_process_segments segment
		LEFT JOIN run_process_activities activity ON activity.segment_id=segment.id
		AND activity.kind<>'final_answer'
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

func (s *Server) loadPendingInteractives(c *gin.Context,
	runID uuid.UUID,
) ([]clientInteractiveSnapshot, error) {
	rows, err := s.db.QueryContext(c, `SELECT request.id,request.status,request.questions,
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

func (s *Server) clientListRunActivities(c *gin.Context) {
	runID, runErr := uuid.Parse(c.Param("runId"))
	segmentID, segmentErr := uuid.Parse(c.Param("segmentId"))
	if runErr != nil || segmentErr != nil {
		badRequest(c, errors.New("Run segment 参数无效"))
		return
	}
	var segmentExists bool
	if err := s.db.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM run_process_segments
		WHERE id=$1 AND run_id=$2)`, segmentID, runID).Scan(&segmentExists); err != nil {
		problem(c, http.StatusInternalServerError, "校验过程分段失败", err)
		return
	}
	if !segmentExists {
		problem(c, http.StatusNotFound, "Run segment 不存在", sql.ErrNoRows)
		return
	}
	if err := s.ensureRunProjection(c, runID); err != nil {
		problem(c, http.StatusInternalServerError, "准备过程动态失败", err)
		return
	}
	limit := 40
	if value, err := strconv.Atoi(c.Query("limit")); err == nil && value > 0 && value <= 100 {
		limit = value
	}
	var before, after int64
	afterProvided := c.Query("afterEventSeq") != ""
	if raw := c.Query("beforeActivitySeq"); raw != "" {
		var parseErr error
		before, parseErr = strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || before <= 0 {
			badRequest(c, errors.New("beforeActivitySeq 无效"))
			return
		}
	}
	if raw := c.Query("afterEventSeq"); raw != "" {
		var parseErr error
		after, parseErr = strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || after < 0 {
			badRequest(c, errors.New("afterEventSeq 无效"))
			return
		}
	}
	if before > 0 && afterProvided {
		badRequest(c, errors.New("活动分页游标不能同时使用"))
		return
	}
	var rows *sql.Rows
	var err error
	if afterProvided {
		rows, err = s.db.QueryContext(c, `SELECT id,item_id,kind,first_event_sequence,
			last_event_sequence,status,payload,occurred_at
			FROM run_process_activities WHERE run_id=$1 AND segment_id=$2
			AND kind<>'final_answer' AND last_event_sequence>$3
			ORDER BY first_event_sequence LIMIT $4`, runID, segmentID, after, limit+1)
	} else {
		if before <= 0 {
			before = int64(^uint64(0) >> 1)
		}
		rows, err = s.db.QueryContext(c, `SELECT id,item_id,kind,first_event_sequence,
			last_event_sequence,status,payload,occurred_at FROM (
				SELECT * FROM run_process_activities WHERE run_id=$1 AND segment_id=$2
				AND kind<>'final_answer' AND first_event_sequence<$3
				ORDER BY first_event_sequence DESC LIMIT $4
			) activity ORDER BY first_event_sequence`, runID, segmentID, before, limit+1)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取过程动态失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientRunActivity, 0, limit+1)
	for rows.Next() {
		var item clientRunActivity
		var occurred sql.NullTime
		if err = rows.Scan(&item.ID, &item.ItemID, &item.Kind, &item.FirstEventSequence,
			&item.LastEventSequence, &item.Status, &item.Payload, &occurred); err != nil {
			problem(c, http.StatusInternalServerError, "解析过程动态失败", err)
			return
		}
		if occurred.Valid {
			item.OccurredAt = occurred.Time.Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	more := len(items) > limit
	if more {
		if afterProvided {
			items = items[:limit]
		} else {
			items = items[1:]
		}
	}
	var watermark int64
	var mode string
	if err = s.db.QueryRowContext(c, `SELECT worker_event_sequence,
		COALESCE(applied_collaboration_mode,collaboration_mode) FROM codex_turn_runs WHERE id=$1`,
		runID).Scan(&watermark, &mode); err != nil {
		problem(c, http.StatusNotFound, "Run 不存在", err)
		return
	}
	persistedThrough := watermark
	if afterProvided && more && len(items) > 0 {
		persistedThrough = items[len(items)-1].LastEventSequence
	}
	var draft any
	if mode != "plan" {
		var itemID string
		var payload json.RawMessage
		if queryErr := s.db.QueryRowContext(c, `SELECT item_id,payload FROM run_process_activities
			WHERE segment_id=$1 AND kind='final_answer' ORDER BY last_event_sequence DESC LIMIT 1`,
			segmentID).Scan(&itemID, &payload); queryErr == nil {
			draft = gin.H{"itemId": itemID, "payload": payload}
		}
	}
	c.JSON(http.StatusOK, gin.H{"activities": items, "hasMoreBefore": !afterProvided && more,
		"hasMoreAfter": afterProvided && more, "persistedThroughEventSeq": persistedThrough,
		"finalAnswerDraft": draft})
}
