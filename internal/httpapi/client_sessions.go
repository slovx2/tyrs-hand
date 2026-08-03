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
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type clientSession struct {
	ID                  uuid.UUID `json:"id"`
	WorkspaceID         uuid.UUID `json:"workspaceId"`
	ProjectID           uuid.UUID `json:"projectId"`
	AgentProfileID      uuid.UUID `json:"agentProfileId"`
	Title               string    `json:"title"`
	LifecycleState      string    `json:"lifecycleState"`
	HistoryCompleteness string    `json:"historyCompleteness"`
	Model               *string   `json:"model"`
	ReasoningEffort     *string   `json:"reasoningEffort"`
	ServiceTier         string    `json:"serviceTier"`
	CollaborationMode   string    `json:"collaborationMode"`
	SettingsVersion     int64     `json:"settingsVersion"`
	LastMessageSeq      int64     `json:"lastMessageSeq"`
	LastActivityAt      time.Time `json:"lastActivityAt"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanClientSession(row rowScanner) (clientSession, error) {
	var result clientSession
	var model, effort sql.NullString
	err := row.Scan(&result.ID, &result.WorkspaceID,
		&result.ProjectID, &result.AgentProfileID, &result.Title,
		&result.LifecycleState, &result.HistoryCompleteness, &model, &effort,
		&result.ServiceTier, &result.CollaborationMode, &result.SettingsVersion,
		&result.LastMessageSeq, &result.LastActivityAt, &result.CreatedAt, &result.UpdatedAt)
	if model.Valid {
		result.Model = &model.String
	}
	if effort.Valid {
		result.ReasoningEffort = &effort.String
	}
	return result, err
}

const clientSessionColumns = `id,workspace_id,workspace_project_id,
	agent_profile_id,title,lifecycle_state,history_completeness,model,reasoning_effort,
	service_tier,collaboration_mode,settings_version,last_message_seq,last_activity_at,
	created_at,updated_at`

const clientSessionQualifiedColumns = `session.id,session.workspace_id,
	session.workspace_project_id,session.agent_profile_id,session.title,
	session.lifecycle_state,session.history_completeness,session.model,
	session.reasoning_effort,session.service_tier,session.collaboration_mode,
	session.settings_version,session.last_message_seq,session.last_activity_at,
	session.created_at,session.updated_at`

func (s *Server) clientBootstrap(c *gin.Context) {
	session := c.MustGet("session").(auth.Session)
	serverID, err := s.clientControlInstanceID(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Control 身份失败", err)
		return
	}
	type option struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	workspaces := make([]option, 0)
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT workspace.id,
		COALESCE(NULLIF(member.display_name,''), member.username)
		FROM worker_workspaces workspace
		JOIN discord_members member ON member.guild_id=workspace.guild_id
			AND member.discord_user_id=workspace.owner_discord_user_id
		ORDER BY lower(COALESCE(NULLIF(member.display_name,''), member.username)), workspace.id`)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item option
			if err = rows.Scan(&item.ID, &item.Name); err != nil {
				break
			}
			workspaces = append(workspaces, item)
		}
	}
	projects := make([]struct {
		ID                 uuid.UUID `json:"id"`
		WorkspaceID        uuid.UUID `json:"workspaceId"`
		Name               string    `json:"name"`
		RelativePath       string    `json:"relativePath"`
		Kind               string    `json:"kind"`
		AvailabilityStatus string    `json:"availabilityStatus"`
		Branch             *string   `json:"branch"`
		Dirty              bool      `json:"dirty"`
	}, 0)
	if err == nil {
		var projectRows *sql.Rows
		projectRows, err = s.db.QueryContext(c.Request.Context(), `SELECT id,workspace_id,name,
			relative_path,project_kind,availability_status,branch,dirty
			FROM workspace_projects ORDER BY lower(name),id`)
		if err == nil {
			defer func() { _ = projectRows.Close() }()
			for projectRows.Next() {
				var item struct {
					ID                 uuid.UUID `json:"id"`
					WorkspaceID        uuid.UUID `json:"workspaceId"`
					Name               string    `json:"name"`
					RelativePath       string    `json:"relativePath"`
					Kind               string    `json:"kind"`
					AvailabilityStatus string    `json:"availabilityStatus"`
					Branch             *string   `json:"branch"`
					Dirty              bool      `json:"dirty"`
				}
				var branch sql.NullString
				if err = projectRows.Scan(&item.ID, &item.WorkspaceID, &item.Name,
					&item.RelativePath, &item.Kind, &item.AvailabilityStatus, &branch,
					&item.Dirty); err != nil {
					break
				}
				item.Branch = nullableString(branch)
				projects = append(projects, item)
			}
		}
	}
	profiles := make([]option, 0)
	if err == nil {
		var profileRows *sql.Rows
		profileRows, err = s.db.QueryContext(c.Request.Context(), `SELECT id,name
			FROM agent_profiles ORDER BY name,id`)
		if err == nil {
			defer func() { _ = profileRows.Close() }()
			for profileRows.Next() {
				var item option
				if err = profileRows.Scan(&item.ID, &item.Name); err != nil {
					break
				}
				profiles = append(profiles, item)
			}
		}
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取客户端启动数据失败", err)
		return
	}
	workspaceIDs := make([]uuid.UUID, 0, len(workspaces))
	for _, workspace := range workspaces {
		workspaceIDs = append(workspaceIDs, workspace.ID)
	}
	catalogs, err := codexcatalog.WorkspaceCatalogs(c.Request.Context(), s.db, workspaceIDs)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Codex 模型目录失败", err)
		return
	}
	modelCatalogs := make(map[string]json.RawMessage, len(catalogs))
	for workspaceID, catalog := range catalogs {
		modelCatalogs[workspaceID.String()] = catalog
	}
	lastSettings, err := loadClientLastSettings(c.Request.Context(), s.db,
		session.AdministratorID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取上次会话参数失败", err)
		return
	}
	if lastSettings == nil && len(profiles) > 0 {
		lastSettings = &clientLastSettings{AgentProfileID: profiles[0].ID,
			CollaborationMode: "default"}
	}
	var currentCursor int64
	if err = s.db.QueryRowContext(c.Request.Context(),
		`SELECT COALESCE(max(cursor),0) FROM client_updates`).Scan(&currentCursor); err != nil {
		problem(c, http.StatusInternalServerError, "读取同步水位失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"serverId": serverID, "protocolVersion": clientProtocolVersion,
		"currentCursor": currentCursor,
		"user":          gin.H{"id": session.AdministratorID, "username": session.Username},
		"capabilities": gin.H{"attachments": true, "pushNotifications": true,
			"sessionLifecycle": true, "planExecution": true},
		"workspaces": workspaces, "projects": projects, "agentProfiles": profiles,
		"modelCatalogs": modelCatalogs, "lastStartedSettings": lastSettings,
	})
}

func optionalClientString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type sessionCursor struct {
	Activity time.Time `json:"activity"`
	ID       uuid.UUID `json:"id"`
}

func decodeSessionCursor(value string) (sessionCursor, error) {
	if value == "" {
		return sessionCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return sessionCursor{}, err
	}
	var result sessionCursor
	err = json.Unmarshal(data, &result)
	return result, err
}

func encodeSessionCursor(value sessionCursor) string {
	data, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(data)
}

func (s *Server) clientListSessions(c *gin.Context) {
	limit := 50
	if value, err := strconv.Atoi(c.Query("limit")); err == nil && value > 0 && value <= 100 {
		limit = value
	}
	cursor, err := decodeSessionCursor(c.Query("cursor"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var projectID any
	if raw := c.Query("projectId"); raw != "" {
		parsed, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			badRequest(c, parseErr)
			return
		}
		projectID = parsed
	}
	lifecycle := c.Query("lifecycle")
	if lifecycle != "" && lifecycle != "active" && lifecycle != "archive_pending" &&
		lifecycle != "archived" && lifecycle != "unarchive_pending" {
		badRequest(c, errors.New("lifecycle 无效"))
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT `+clientSessionColumns+`
		FROM workspace_sessions
		WHERE ($1::timestamptz IS NULL OR (last_activity_at,id) < ($1,$2))
		  AND ($4::uuid IS NULL OR workspace_project_id=$4)
		  AND ($5='' OR lifecycle_state=$5)
		ORDER BY last_activity_at DESC,id DESC LIMIT $3`, clientCursorTime(cursor.Activity),
		clientCursorUUID(cursor.ID), limit+1, projectID, lifecycle)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Session 失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientSession, 0, limit)
	for rows.Next() {
		item, scanErr := scanClientSession(rows)
		if scanErr != nil {
			problem(c, http.StatusInternalServerError, "解析 Session 失败", scanErr)
			return
		}
		items = append(items, item)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeSessionCursor(sessionCursor{Activity: last.LastActivityAt, ID: last.ID})
	}
	c.JSON(http.StatusOK, gin.H{"sessions": items, "nextCursor": next})
}

func clientCursorTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func clientCursorUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

type createClientSessionRequest struct {
	ProjectID      uuid.UUID             `json:"projectId" binding:"required"`
	Settings       clientSessionSettings `json:"settings" binding:"required"`
	InitialMessage struct {
		LocalID       string      `json:"localId" binding:"required"`
		Text          string      `json:"text" binding:"required"`
		AttachmentIDs []uuid.UUID `json:"attachmentIds"`
	} `json:"initialMessage" binding:"required"`
}

func (s *Server) clientCreateSession(c *gin.Context) {
	var request createClientSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.InitialMessage.LocalID = strings.TrimSpace(request.InitialMessage.LocalID)
	request.InitialMessage.Text = strings.TrimSpace(request.InitialMessage.Text)
	if request.InitialMessage.LocalID == "" || len(request.InitialMessage.LocalID) > 200 ||
		request.InitialMessage.Text == "" || len(request.InitialMessage.AttachmentIDs) > 10 {
		badRequest(c, errors.New("首条消息或附件无效"))
		return
	}
	if request.Settings.ServiceTier == "" {
		request.Settings.ServiceTier = "standard"
	}
	if request.Settings.CollaborationMode == "" {
		request.Settings.CollaborationMode = "default"
	}
	if err := validateClientSettings(request.Settings.Model, request.Settings.ReasoningEffort,
		request.Settings.ServiceTier, request.Settings.CollaborationMode); err != nil {
		badRequest(c, err)
		return
	}
	administrator := c.MustGet("session").(auth.Session)
	var deviceID uuid.UUID
	if len(request.InitialMessage.AttachmentIDs) > 0 {
		var ok bool
		deviceID, ok = clientRequestDeviceID(c)
		if !ok {
			return
		}
	}
	idempotencyKey := "client:session:" + administrator.AdministratorID.String() + ":" +
		request.InitialMessage.LocalID
	if existing, found, err := s.findClientCreatedSession(c, idempotencyKey); err != nil {
		problem(c, http.StatusInternalServerError, "检查会话幂等键失败", err)
		return
	} else if found {
		c.JSON(http.StatusOK, gin.H{"session": existing, "deduplicated": true})
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var workspaceID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT project.workspace_id
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		WHERE project.id=$1 AND project.availability_status='available'
		  FOR SHARE`, request.ProjectID).
		Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusUnprocessableEntity, "项目当前不可用", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取项目失败", err)
		return
	}
	row := tx.QueryRowContext(c.Request.Context(), `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,
		created_by_administrator_id,title,model,reasoning_effort,service_tier,collaboration_mode,
		settings_version,title_revision,title_source)
		SELECT $1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,1,0,'fallback'
		WHERE EXISTS(SELECT 1 FROM agent_profiles WHERE id=$3)
		RETURNING `+clientSessionColumns, workspaceID, request.ProjectID,
		request.Settings.AgentProfileID, administrator.AdministratorID,
		fallbackSessionTitle(request.InitialMessage.Text), stringValue(request.Settings.Model),
		stringValue(request.Settings.ReasoningEffort), request.Settings.ServiceTier,
		request.Settings.CollaborationMode)
	created, err := scanClientSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusUnprocessableEntity, "Workspace、项目或 Agent Profile 不可用", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Session 失败", err)
		return
	}
	err = upsertClientParticipant(c, tx, administrator)
	if err == nil {
		repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
			s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
		_, inserted, enqueueErr := repository.Enqueue(c.Request.Context(), tx,
			codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace,
				SessionID: created.ID, InputSurface: "client",
				IdempotencyKey: idempotencyKey, MessageLocalID: request.InitialMessage.LocalID,
				Instruction: request.InitialMessage.Text, Behavior: "start_when_idle",
				ReplyPolicy: "silent", ActorLogin: administrator.Username,
				ActorPermission: "owner", ActorParticipantID: administrator.AdministratorID,
				ActorDisplayName: administrator.Username})
		if enqueueErr != nil {
			err = enqueueErr
		} else if !inserted {
			err = errors.New("会话启动幂等键冲突")
		}
	}
	if err == nil && len(request.InitialMessage.AttachmentIDs) > 0 {
		var messageID uuid.UUID
		err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM session_messages
			WHERE session_id=$1 AND local_id=$2`, created.ID,
			request.InitialMessage.LocalID).Scan(&messageID)
		if err == nil {
			err = linkClientAttachmentsTx(c.Request.Context(), tx, created.ID, messageID,
				deviceID, request.InitialMessage.AttachmentIDs)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_user_preferences(
			administrator_id,agent_profile_id,model,reasoning_effort,service_tier,
			collaboration_mode) VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6)
			ON CONFLICT(administrator_id) DO UPDATE SET agent_profile_id=EXCLUDED.agent_profile_id,
			model=EXCLUDED.model,reasoning_effort=EXCLUDED.reasoning_effort,
			service_tier=EXCLUDED.service_tier,collaboration_mode=EXCLUDED.collaboration_mode,
			version=client_user_preferences.version+1,updated_at=now()`,
			administrator.AdministratorID, request.Settings.AgentProfileID,
			stringValue(request.Settings.Model), stringValue(request.Settings.ReasoningEffort),
			request.Settings.ServiceTier, request.Settings.CollaborationMode)
	}
	if err == nil {
		version := created.SettingsVersion
		_, err = insertClientUpdate(c.Request.Context(), tx, &created.ID, "session.created",
			"session", created.ID.String(), nil, &version, created)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Session 失败", err)
		return
	}
	if s.redis != nil {
		_ = s.redis.Publish(c.Request.Context(), codexcontrol.WakeupChannel, "queued").Err()
	}
	c.JSON(http.StatusCreated, gin.H{"session": created, "deduplicated": false})
}

func (s *Server) findClientCreatedSession(c *gin.Context, idempotencyKey string) (
	clientSession, bool, error,
) {
	result, err := scanClientSession(s.db.QueryRowContext(c.Request.Context(), `SELECT `+
		clientSessionQualifiedColumns+`
		FROM codex_turn_intents intent
		JOIN workspace_sessions session ON session.id=intent.session_id
		WHERE intent.idempotency_key=$1`, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return clientSession{}, false, nil
	}
	return result, err == nil, err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
