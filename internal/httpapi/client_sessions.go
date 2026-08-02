package httpapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

type clientSession struct {
	ID                       uuid.UUID `json:"id"`
	DevelopmentEnvironmentID uuid.UUID `json:"developmentEnvironmentId"`
	DevelopmentProjectID     uuid.UUID `json:"developmentProjectId"`
	AgentProfileID           uuid.UUID `json:"agentProfileId"`
	Title                    string    `json:"title"`
	LifecycleState           string    `json:"lifecycleState"`
	HistoryCompleteness      string    `json:"historyCompleteness"`
	Model                    *string   `json:"model"`
	ReasoningEffort          *string   `json:"reasoningEffort"`
	ServiceTier              string    `json:"serviceTier"`
	CollaborationMode        string    `json:"collaborationMode"`
	SettingsVersion          int64     `json:"settingsVersion"`
	LastMessageSeq           int64     `json:"lastMessageSeq"`
	LastActivityAt           time.Time `json:"lastActivityAt"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanClientSession(row rowScanner) (clientSession, error) {
	var result clientSession
	var model, effort sql.NullString
	err := row.Scan(&result.ID, &result.DevelopmentEnvironmentID,
		&result.DevelopmentProjectID, &result.AgentProfileID, &result.Title,
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

const clientSessionColumns = `id,development_environment_id,development_project_id,
	agent_profile_id,title,lifecycle_state,history_completeness,model,reasoning_effort,
	service_tier,collaboration_mode,settings_version,last_message_seq,last_activity_at,
	created_at,updated_at`

func (s *Server) clientBootstrap(c *gin.Context) {
	session := c.MustGet("session").(auth.Session)
	type option struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	environments := make([]option, 0)
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id,container_name
		FROM discord_development_environments WHERE status='running' ORDER BY container_name,id`)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item option
			if err = rows.Scan(&item.ID, &item.Name); err != nil {
				break
			}
			environments = append(environments, item)
		}
	}
	projects := make([]struct {
		ID            uuid.UUID `json:"id"`
		EnvironmentID uuid.UUID `json:"environmentId"`
		Name          string    `json:"name"`
	}, 0)
	if err == nil {
		var projectRows *sql.Rows
		projectRows, err = s.db.QueryContext(c.Request.Context(), `SELECT id,environment_id,name
			FROM development_projects WHERE availability_status='available' ORDER BY name,id`)
		if err == nil {
			defer func() { _ = projectRows.Close() }()
			for projectRows.Next() {
				var item struct {
					ID            uuid.UUID `json:"id"`
					EnvironmentID uuid.UUID `json:"environmentId"`
					Name          string    `json:"name"`
				}
				if err = projectRows.Scan(&item.ID, &item.EnvironmentID, &item.Name); err != nil {
					break
				}
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
	c.JSON(http.StatusOK, gin.H{
		"user":         gin.H{"id": session.AdministratorID, "username": session.Username},
		"environments": environments, "projects": projects, "agentProfiles": profiles,
	})
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
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT `+clientSessionColumns+`
		FROM development_sessions
		WHERE ($1::timestamptz IS NULL OR (last_activity_at,id) < ($1,$2))
		ORDER BY last_activity_at DESC,id DESC LIMIT $3`, clientCursorTime(cursor.Activity),
		clientCursorUUID(cursor.ID), limit+1)
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
	DevelopmentEnvironmentID uuid.UUID `json:"developmentEnvironmentId" binding:"required"`
	DevelopmentProjectID     uuid.UUID `json:"developmentProjectId" binding:"required"`
	AgentProfileID           uuid.UUID `json:"agentProfileId" binding:"required"`
	Title                    string    `json:"title"`
	Model                    *string   `json:"model"`
	ReasoningEffort          *string   `json:"reasoningEffort"`
	ServiceTier              string    `json:"serviceTier"`
	CollaborationMode        string    `json:"collaborationMode"`
}

func (s *Server) clientCreateSession(c *gin.Context) {
	var request createClientSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.ServiceTier == "" {
		request.ServiceTier = "standard"
	}
	if request.CollaborationMode == "" {
		request.CollaborationMode = "default"
	}
	administrator := c.MustGet("session").(auth.Session)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(c.Request.Context(), `INSERT INTO development_sessions(
		development_environment_id,development_project_id,agent_profile_id,
		created_by_administrator_id,title,model,reasoning_effort,service_tier,collaboration_mode)
		SELECT $1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9
		FROM development_projects project
		JOIN discord_development_environments environment ON environment.id=project.environment_id
		WHERE project.id=$2 AND project.environment_id=$1
		  AND project.availability_status='available' AND environment.status='running'
		  AND EXISTS(SELECT 1 FROM agent_profiles WHERE id=$3)
		RETURNING `+clientSessionColumns, request.DevelopmentEnvironmentID,
		request.DevelopmentProjectID, request.AgentProfileID, administrator.AdministratorID,
		request.Title, stringValue(request.Model), stringValue(request.ReasoningEffort),
		request.ServiceTier, request.CollaborationMode)
	created, err := scanClientSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusUnprocessableEntity, "开发环境、项目或 Agent Profile 不可用", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Session 失败", err)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO participants(id,kind,display_name)
		VALUES ($1,'administrator',$2) ON CONFLICT(id) DO UPDATE SET
		display_name=EXCLUDED.display_name,updated_at=now()`, administrator.AdministratorID,
		administrator.Username)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO participant_identities(
			participant_id,provider,external_key) VALUES ($1,'administrator',$2)
			ON CONFLICT(provider,external_key) DO NOTHING`, administrator.AdministratorID,
			administrator.AdministratorID.String())
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
			session_id,update_type,entity_id,entity_seq,payload)
			VALUES ($1::uuid,'session.created',($1::uuid)::text,0,
				jsonb_build_object('sessionId',($1::uuid)::text))`, created.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Session 失败", err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
