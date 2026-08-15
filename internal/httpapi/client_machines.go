package httpapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientMachine struct {
	WorkerID              uuid.UUID  `json:"workerId"`
	Name                  string     `json:"name"`
	SSHHostKeyFingerprint string     `json:"sshHostKeyFingerprint"`
	Status                string     `json:"status"`
	HeartbeatAt           *time.Time `json:"heartbeatAt,omitempty"`
	WorkspaceID           *uuid.UUID `json:"workspaceId,omitempty"`
	ApprovedAt            time.Time  `json:"approvedAt"`
}

type clientScheduledTaskProject struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	RelativePath       string    `json:"relativePath"`
	ProjectKind        string    `json:"projectKind"`
	AvailabilityStatus string    `json:"availabilityStatus"`
}

type clientScheduledTaskSession struct {
	ID               uuid.UUID `json:"id"`
	Title            string    `json:"title"`
	ExternalThreadID *string   `json:"externalThreadId,omitempty"`
}

type clientScheduledTaskSettings struct {
	AgentProfileID  uuid.UUID `json:"agentProfileId"`
	Model           *string   `json:"model,omitempty"`
	ReasoningEffort *string   `json:"reasoningEffort,omitempty"`
	ServiceTier     *string   `json:"serviceTier,omitempty"`
}

type clientScheduledTask struct {
	ID                 uuid.UUID                    `json:"id"`
	WorkspaceID        uuid.UUID                    `json:"workspaceId"`
	Kind               string                       `json:"kind"`
	Name               string                       `json:"name"`
	Prompt             string                       `json:"prompt"`
	Status             string                       `json:"status"`
	Schedule           string                       `json:"schedule"`
	Timezone           string                       `json:"timezone"`
	ScheduleKind       string                       `json:"scheduleKind"`
	IntervalSeconds    *int64                       `json:"intervalSeconds,omitempty"`
	NextRunAt          *time.Time                   `json:"nextRunAt,omitempty"`
	BlockedUntil       *time.Time                   `json:"blockedUntil,omitempty"`
	LastRunAt          *time.Time                   `json:"lastRunAt,omitempty"`
	ScheduleRevision   int64                        `json:"scheduleRevision"`
	LastErrorCode      *string                      `json:"lastErrorCode,omitempty"`
	LastErrorMessage   *string                      `json:"lastErrorMessage,omitempty"`
	Project            clientScheduledTaskProject   `json:"project"`
	TargetSession      *clientScheduledTaskSession  `json:"targetSession,omitempty"`
	StandaloneSettings *clientScheduledTaskSettings `json:"standaloneSettings,omitempty"`
	CreatedAt          time.Time                    `json:"createdAt"`
	UpdatedAt          time.Time                    `json:"updatedAt"`
}

type clientScheduledTaskRun struct {
	ID               uuid.UUID                   `json:"id"`
	ScheduleRevision int64                       `json:"scheduleRevision"`
	Trigger          string                      `json:"trigger"`
	TriggerKey       string                      `json:"triggerKey"`
	ScheduledFor     time.Time                   `json:"scheduledFor"`
	CoalescedThrough *time.Time                  `json:"coalescedThrough,omitempty"`
	Status           string                      `json:"status"`
	ErrorCode        *string                     `json:"errorCode,omitempty"`
	ErrorMessage     *string                     `json:"errorMessage,omitempty"`
	StartedAt        *time.Time                  `json:"startedAt,omitempty"`
	FinishedAt       *time.Time                  `json:"finishedAt,omitempty"`
	Session          *clientScheduledTaskSession `json:"session,omitempty"`
	CreatedAt        time.Time                   `json:"createdAt"`
	UpdatedAt        time.Time                   `json:"updatedAt"`
}

func clientNullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func clientNullableUUID(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	result := value.UUID
	return &result
}

func (s *Server) requireClientMachine(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	workerID, err := uuid.Parse(c.Param("workerId"))
	if err != nil {
		badRequest(c, err)
		return uuid.Nil, uuid.Nil, false
	}
	var authorized bool
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT true
		FROM client_device_workers binding JOIN workers worker ON worker.id=binding.worker_id
		WHERE binding.device_id=$1 AND binding.worker_id=$2
			AND binding.ssh_host_key_fingerprint=worker.ssh_host_key_fingerprint`,
		deviceID, workerID).Scan(&authorized)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "机器不存在或未授权", err)
		return uuid.Nil, uuid.Nil, false
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "验证机器授权失败", err)
		return uuid.Nil, uuid.Nil, false
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE client_device_workers
		SET last_seen_at=now(),updated_at=now() WHERE device_id=$1 AND worker_id=$2`,
		deviceID, workerID)
	return deviceID, workerID, true
}

func (s *Server) listClientMachines(c *gin.Context) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT worker.id,worker.name,
		binding.ssh_host_key_fingerprint,worker.status,worker.heartbeat_at,workspace.id,
		binding.approved_at
		FROM client_device_workers binding JOIN workers worker ON worker.id=binding.worker_id
		LEFT JOIN worker_workspaces workspace ON workspace.worker_id=worker.id
		WHERE binding.device_id=$1
			AND binding.ssh_host_key_fingerprint=worker.ssh_host_key_fingerprint
		ORDER BY worker.name,worker.id`, deviceID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取机器列表失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientMachine, 0)
	for rows.Next() {
		var item clientMachine
		var heartbeat sql.NullTime
		var workspaceID uuid.NullUUID
		if err := rows.Scan(&item.WorkerID, &item.Name, &item.SSHHostKeyFingerprint,
			&item.Status, &heartbeat, &workspaceID, &item.ApprovedAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取机器列表失败", err)
			return
		}
		item.HeartbeatAt = clientNullableTime(heartbeat)
		item.WorkspaceID = clientNullableUUID(workspaceID)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取机器列表失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) deleteClientMachine(c *gin.Context) {
	deviceID, workerID, ok := s.requireClientMachine(c)
	if !ok {
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM client_device_workers
		WHERE device_id=$1 AND worker_id=$2`, deviceID, workerID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "移除机器授权失败", err)
		return
	}
	deleted, _ := result.RowsAffected()
	if deleted != 1 {
		problem(c, http.StatusNotFound, "机器授权不存在", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

type clientPageCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        uuid.UUID `json:"id"`
}

func clientPageLimit(c *gin.Context) (int, bool) {
	value := strings.TrimSpace(c.Query("limit"))
	if value == "" {
		return 50, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		badRequest(c, errors.New("limit 必须在 1 到 100 之间"))
		return 0, false
	}
	return limit, true
}

func decodeClientPageCursor(value string) (*clientPageCursor, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("cursor 无效")
	}
	var cursor clientPageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return nil, errors.New("cursor 无效")
	}
	return &cursor, nil
}

func encodeClientPageCursor(createdAt time.Time, id uuid.UUID) string {
	raw, _ := json.Marshal(clientPageCursor{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

const clientScheduledTaskSelect = `SELECT task.id,task.workspace_id,task.kind,task.name,
	task.prompt,task.status,task.schedule_text,task.timezone,task.schedule_kind,
	task.interval_seconds,task.next_run_at,task.blocked_until,task.last_run_at,
	task.schedule_revision,task.last_error_code,task.last_error_message,
	task.agent_profile_id,task.model,task.reasoning_effort,task.service_tier,
	task.created_at,task.updated_at,project.id,project.name,project.relative_path,
	project.project_kind,project.availability_status,target.id,target.title,
	target_control.external_thread_id
	FROM scheduled_tasks task
	JOIN worker_workspaces workspace ON workspace.id=task.workspace_id
	JOIN workspace_projects project ON project.id=task.workspace_project_id
	LEFT JOIN workspace_sessions target ON target.id=task.target_session_id
	LEFT JOIN codex_thread_controls target_control ON target_control.session_id=target.id`

type clientRowScanner interface {
	Scan(...any) error
}

func scanClientScheduledTask(row clientRowScanner) (clientScheduledTask, error) {
	var result clientScheduledTask
	var interval sql.NullInt64
	var nextRun, blockedUntil, lastRun sql.NullTime
	var errorCode, errorMessage sql.NullString
	var profileID, targetID uuid.NullUUID
	var model, effort, tier, targetTitle, externalThread sql.NullString
	err := row.Scan(&result.ID, &result.WorkspaceID, &result.Kind, &result.Name,
		&result.Prompt, &result.Status, &result.Schedule, &result.Timezone,
		&result.ScheduleKind, &interval, &nextRun, &blockedUntil, &lastRun,
		&result.ScheduleRevision, &errorCode, &errorMessage, &profileID, &model, &effort,
		&tier, &result.CreatedAt, &result.UpdatedAt, &result.Project.ID,
		&result.Project.Name, &result.Project.RelativePath, &result.Project.ProjectKind,
		&result.Project.AvailabilityStatus, &targetID, &targetTitle, &externalThread)
	if err != nil {
		return clientScheduledTask{}, err
	}
	if interval.Valid {
		value := interval.Int64
		result.IntervalSeconds = &value
	}
	result.NextRunAt = clientNullableTime(nextRun)
	result.BlockedUntil = clientNullableTime(blockedUntil)
	result.LastRunAt = clientNullableTime(lastRun)
	result.LastErrorCode = nullableString(errorCode)
	result.LastErrorMessage = nullableString(errorMessage)
	if profileID.Valid {
		result.StandaloneSettings = &clientScheduledTaskSettings{
			AgentProfileID: profileID.UUID, Model: nullableString(model),
			ReasoningEffort: nullableString(effort), ServiceTier: nullableString(tier),
		}
	}
	if targetID.Valid {
		result.TargetSession = &clientScheduledTaskSession{ID: targetID.UUID,
			Title: targetTitle.String, ExternalThreadID: nullableString(externalThread)}
	}
	return result, nil
}

func validateClientTaskStatus(value string) error {
	if value == "" || value == "active" || value == "paused" || value == "completed" ||
		value == "deleted" {
		return nil
	}
	return errors.New("status 无效")
}

func (s *Server) listClientMachineScheduledTasks(c *gin.Context) {
	_, workerID, ok := s.requireClientMachine(c)
	if !ok {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if err := validateClientTaskStatus(status); err != nil {
		badRequest(c, err)
		return
	}
	limit, ok := clientPageLimit(c)
	if !ok {
		return
	}
	cursor, err := decodeClientPageCursor(strings.TrimSpace(c.Query("cursor")))
	if err != nil {
		badRequest(c, err)
		return
	}
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.QueryContext(c.Request.Context(), clientScheduledTaskSelect+`
		WHERE workspace.worker_id=$1
			AND (($2='') OR task.status=$2)
			AND (($2<>'') OR task.status<>'deleted')
			AND ($3::timestamptz IS NULL OR
				(task.created_at,task.id)<($3::timestamptz,$4::uuid))
		ORDER BY task.created_at DESC,task.id DESC LIMIT $5`, workerID, status,
		cursorTime, cursorID, limit)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取定时任务失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientScheduledTask, 0, limit)
	for rows.Next() {
		item, err := scanClientScheduledTask(rows)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取定时任务失败", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取定时任务失败", err)
		return
	}
	response := gin.H{"items": items}
	if len(items) == limit {
		last := items[len(items)-1]
		response["nextCursor"] = encodeClientPageCursor(last.CreatedAt, last.ID)
	}
	c.JSON(http.StatusOK, response)
}

func (s *Server) getClientMachineScheduledTask(c *gin.Context) {
	_, workerID, ok := s.requireClientMachine(c)
	if !ok {
		return
	}
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	result, err := scanClientScheduledTask(s.db.QueryRowContext(c.Request.Context(),
		clientScheduledTaskSelect+` WHERE workspace.worker_id=$1 AND task.id=$2`,
		workerID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "定时任务不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取定时任务失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func scanClientScheduledTaskRun(row clientRowScanner) (clientScheduledTaskRun, error) {
	var result clientScheduledTaskRun
	var coalesced, started, finished sql.NullTime
	var errorCode, errorMessage sql.NullString
	var sessionID uuid.NullUUID
	var sessionTitle, externalThread sql.NullString
	err := row.Scan(&result.ID, &result.ScheduleRevision, &result.Trigger,
		&result.TriggerKey, &result.ScheduledFor, &coalesced, &result.Status,
		&errorCode, &errorMessage, &started, &finished, &sessionID, &sessionTitle,
		&externalThread, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return clientScheduledTaskRun{}, err
	}
	result.CoalescedThrough = clientNullableTime(coalesced)
	result.ErrorCode = nullableString(errorCode)
	result.ErrorMessage = nullableString(errorMessage)
	result.StartedAt = clientNullableTime(started)
	result.FinishedAt = clientNullableTime(finished)
	if sessionID.Valid {
		result.Session = &clientScheduledTaskSession{ID: sessionID.UUID,
			Title: sessionTitle.String, ExternalThreadID: nullableString(externalThread)}
	}
	return result, nil
}

func (s *Server) listClientMachineScheduledTaskRuns(c *gin.Context) {
	_, workerID, ok := s.requireClientMachine(c)
	if !ok {
		return
	}
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	limit, ok := clientPageLimit(c)
	if !ok {
		return
	}
	cursor, err := decodeClientPageCursor(strings.TrimSpace(c.Query("cursor")))
	if err != nil {
		badRequest(c, err)
		return
	}
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT run.id,
		run.schedule_revision,run.trigger,run.trigger_key,run.scheduled_for,
		run.coalesced_through,run.status,run.error_code,run.error_message,
		run.started_at,run.finished_at,session.id,session.title,
		control.external_thread_id,run.created_at,run.updated_at
		FROM scheduled_task_runs run
		JOIN scheduled_tasks task ON task.id=run.scheduled_task_id
		JOIN worker_workspaces workspace ON workspace.id=task.workspace_id
		LEFT JOIN workspace_sessions session ON session.id=run.session_id
		LEFT JOIN codex_turn_intents intent ON intent.id=run.intent_id
		LEFT JOIN codex_thread_controls control ON control.id=intent.control_id
		WHERE workspace.worker_id=$1 AND task.id=$2
			AND ($3::timestamptz IS NULL OR
				(run.created_at,run.id)<($3::timestamptz,$4::uuid))
		ORDER BY run.created_at DESC,run.id DESC LIMIT $5`, workerID, taskID,
		cursorTime, cursorID, limit)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取定时任务运行记录失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientScheduledTaskRun, 0, limit)
	for rows.Next() {
		item, err := scanClientScheduledTaskRun(rows)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取定时任务运行记录失败", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取定时任务运行记录失败", err)
		return
	}
	if len(items) == 0 {
		var exists bool
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT true
			FROM scheduled_tasks task JOIN worker_workspaces workspace
				ON workspace.id=task.workspace_id
			WHERE workspace.worker_id=$1 AND task.id=$2`, workerID, taskID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusNotFound, "定时任务不存在", err)
			return
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取定时任务失败", err)
			return
		}
	}
	response := gin.H{"items": items}
	if len(items) == limit {
		last := items[len(items)-1]
		response["nextCursor"] = encodeClientPageCursor(last.CreatedAt, last.ID)
	}
	c.JSON(http.StatusOK, response)
}
