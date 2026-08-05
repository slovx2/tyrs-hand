package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientRunSettings struct {
	Model             *string `json:"model"`
	ReasoningEffort   *string `json:"reasoningEffort"`
	ServiceTier       *string `json:"serviceTier"`
	CollaborationMode string  `json:"collaborationMode"`
	SettingsVersion   int64   `json:"settingsVersion"`
}

type clientRunSnapshot struct {
	ID                  uuid.UUID                   `json:"id"`
	Status              string                      `json:"status"`
	ActualSettings      clientRunSettings           `json:"actualSettings"`
	StartedAt           time.Time                   `json:"startedAt"`
	FinishedAt          *time.Time                  `json:"finishedAt"`
	ErrorCode           *string                     `json:"errorCode"`
	ErrorMessage        *string                     `json:"errorMessage"`
	Timeline            []clientTimelineEvent       `json:"timeline"`
	PendingInteractives []clientInteractiveSnapshot `json:"pendingInteractives"`
}

type clientTimelineEvent struct {
	Sequence   int64           `json:"sequence"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurredAt"`
}

type clientInteractiveSnapshot struct {
	ID         uuid.UUID       `json:"id"`
	Status     string          `json:"status"`
	Questions  json.RawMessage `json:"questions"`
	Secret     bool            `json:"secret"`
	DeadlineAt *time.Time      `json:"deadlineAt"`
}

type clientSessionDetail struct {
	Session    clientSession         `json:"session"`
	Settings   clientSessionSettings `json:"settings"`
	CurrentRun *clientRunSnapshot    `json:"currentRun"`
}

func (s *Server) clientGetSession(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	detail, err := s.loadClientSessionDetail(c, id)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Session 失败", err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) loadClientSessionDetail(c *gin.Context, id uuid.UUID) (clientSessionDetail, error) {
	session, err := scanClientSessionSummary(s.db.QueryRowContext(c.Request.Context(),
		`SELECT `+clientSessionSummaryColumns+` FROM workspace_sessions session WHERE session.id=$1`, id))
	if err != nil {
		return clientSessionDetail{}, err
	}
	detail := clientSessionDetail{Session: session, Settings: clientSessionSettings{
		AgentProfileID: session.AgentProfileID, Model: session.Model,
		ReasoningEffort: session.ReasoningEffort, ServiceTier: session.ServiceTier,
		CollaborationMode: session.CollaborationMode, SettingsVersion: session.SettingsVersion,
	}}
	var run clientRunSnapshot
	var model, effort, tier, errorCode, errorMessage sql.NullString
	var finished sql.NullTime
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT run.id,run.status,
		COALESCE(run.applied_model,run.model),
		COALESCE(run.applied_reasoning_effort,run.reasoning_effort),
		COALESCE(run.applied_service_tier,run.service_tier),
		COALESCE(run.applied_collaboration_mode,run.collaboration_mode),
		COALESCE(run.applied_settings_revision,run.settings_revision),run.started_at,
		run.finished_at,run.error_code,run.error_message
		FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id=run.control_id
		WHERE control.session_id=$1 ORDER BY run.started_at DESC LIMIT 1`, id).
		Scan(&run.ID, &run.Status, &model, &effort, &tier,
			&run.ActualSettings.CollaborationMode, &run.ActualSettings.SettingsVersion,
			&run.StartedAt, &finished, &errorCode, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, nil
	}
	if err != nil {
		return clientSessionDetail{}, err
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
	run.PendingInteractives = make([]clientInteractiveSnapshot, 0)
	interactiveRows, queryErr := s.db.QueryContext(c.Request.Context(), `SELECT id,status,questions,deadline_at
		FROM codex_interactive_requests WHERE run_id=$1 AND status='pending' ORDER BY created_at`, run.ID)
	if queryErr != nil {
		return clientSessionDetail{}, queryErr
	}
	defer func() { _ = interactiveRows.Close() }()
	for interactiveRows.Next() {
		var item clientInteractiveSnapshot
		var deadline sql.NullTime
		if queryErr = interactiveRows.Scan(&item.ID, &item.Status, &item.Questions,
			&deadline); queryErr != nil {
			return clientSessionDetail{}, queryErr
		}
		item.Secret = interactiveQuestionsSecret(item.Questions)
		if deadline.Valid {
			item.DeadlineAt = &deadline.Time
		}
		run.PendingInteractives = append(run.PendingInteractives, item)
	}
	if queryErr = interactiveRows.Err(); queryErr != nil {
		return clientSessionDetail{}, queryErr
	}
	detail.CurrentRun = &run
	return detail, nil
}

type patchClientSessionRequest struct {
	Title                   *string    `json:"title"`
	AgentProfileID          *uuid.UUID `json:"agentProfileId"`
	Model                   *string    `json:"model"`
	ReasoningEffort         *string    `json:"reasoningEffort"`
	ServiceTier             *string    `json:"serviceTier"`
	CollaborationMode       *string    `json:"collaborationMode"`
	ExpectedSettingsVersion *int64     `json:"expectedSettingsVersion"`
}

func (s *Server) clientPatchSession(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request patchClientSessionRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "修改 Session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanClientSession(tx.QueryRowContext(c.Request.Context(),
		`SELECT `+clientSessionColumns+` FROM workspace_sessions WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Session 失败", err)
		return
	}
	settingsChanged := request.AgentProfileID != nil || request.Model != nil ||
		request.ReasoningEffort != nil || request.ServiceTier != nil ||
		request.CollaborationMode != nil
	if request.ExpectedSettingsVersion != nil &&
		*request.ExpectedSettingsVersion != current.SettingsVersion {
		problem(c, http.StatusConflict, "会话参数已在其他位置更新", nil)
		return
	}
	profileID, model, effort := current.AgentProfileID, current.Model, current.ReasoningEffort
	tier, mode := current.ServiceTier, current.CollaborationMode
	if request.AgentProfileID != nil {
		profileID = *request.AgentProfileID
	}
	if request.Model != nil {
		model = request.Model
	}
	if request.ReasoningEffort != nil {
		effort = request.ReasoningEffort
	}
	if request.ServiceTier != nil {
		tier = *request.ServiceTier
	}
	if request.CollaborationMode != nil {
		mode = *request.CollaborationMode
	}
	if settingsChanged {
		if err = validateClientSettings(model, effort, tier, mode); err != nil {
			badRequest(c, err)
			return
		}
		if profileID == uuid.Nil {
			badRequest(c, errors.New("Agent Profile 无效"))
			return
		}
	}
	title := current.Title
	manualTitle := request.Title != nil
	if manualTitle {
		title = strings.TrimSpace(*request.Title)
		if title == "" || len([]rune(title)) > 120 {
			badRequest(c, errors.New("标题长度无效"))
			return
		}
	}
	row := tx.QueryRowContext(c.Request.Context(), `UPDATE workspace_sessions SET
		title=$2,agent_profile_id=$3,model=NULLIF($4,''),reasoning_effort=NULLIF($5,''),
		service_tier=$6,collaboration_mode=$7,
		settings_version=settings_version+CASE WHEN $8 THEN 1 ELSE 0 END,
		title_revision=title_revision+CASE WHEN $9 THEN 1 ELSE 0 END,
		title_source=CASE WHEN $9 THEN 'manual' ELSE title_source END,updated_at=now()
		WHERE id=$1 AND EXISTS(SELECT 1 FROM agent_profiles WHERE id=$3)
		RETURNING `+clientSessionColumns, id, title, profileID, stringValue(model),
		stringValue(effort), tier, mode, settingsChanged, manualTitle)
	updated, err := scanClientSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusUnprocessableEntity, "Agent Profile 不存在", err)
		return
	}
	if err == nil && settingsChanged {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			agent_profile_id=$2,model=NULLIF($3,''),reasoning_effort=NULLIF($4,''),
			service_tier=$5,collaboration_mode=$6,settings_revision=$7,
			runtime_preferences_frozen_at=now(),updated_at=now() WHERE session_id=$1`,
			id, profileID, stringValue(model), stringValue(effort), tier, mode,
			updated.SettingsVersion)
	}
	if err == nil {
		updated, err = scanClientSessionSummary(tx.QueryRowContext(c.Request.Context(),
			`SELECT `+clientSessionSummaryColumns+` FROM workspace_sessions session WHERE session.id=$1`, id))
	}
	if err == nil {
		version := updated.SettingsVersion
		_, err = insertClientUpdate(c.Request.Context(), tx, &id, "session.updated",
			"session", id.String(), nil, &version, updated)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "修改 Session 失败", err)
		return
	}
	c.JSON(http.StatusOK, updated)
}
