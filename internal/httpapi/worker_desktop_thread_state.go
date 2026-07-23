package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) loadDesktopThreadState(c *gin.Context,
	requestID uuid.UUID,
) (workerprotocol.DesktopThreadState, error) {
	var state workerprotocol.DesktopThreadState
	var forumID, conversationID, controlID sql.NullString
	var response sql.NullString
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT r.id, r.environment_id,
		r.operation, r.status, r.forum_id::text, r.conversation_id::text, r.control_id::text,
		COALESCE(r.external_thread_id,''), r.response, COALESCE(r.error,'')
		FROM desktop_thread_requests r JOIN discord_development_environments e ON e.id = r.environment_id
		WHERE r.id = $1 AND e.execution_node_id = $2`, requestID, workerNode(c).ID).
		Scan(&state.ID, &state.EnvironmentID, &state.Operation, &state.Status, &forumID,
			&conversationID, &controlID, &state.ExternalThreadID, &response, &state.Error)
	if err != nil {
		return state, err
	}
	state.ForumID = parseOptionalUUID(forumID)
	state.ConversationID = parseOptionalUUID(conversationID)
	state.ControlID = parseOptionalUUID(controlID)
	if response.Valid {
		state.Response = json.RawMessage(response.String)
	}
	state.Config, err = s.desktopThreadConfig(c, state)
	return state, err
}

func (s *Server) desktopThreadConfig(c *gin.Context,
	state workerprotocol.DesktopThreadState,
) (workerprotocol.DesktopThreadConfig, error) {
	var result workerprotocol.DesktopThreadConfig
	var profileID uuid.UUID
	var allowed, dangerous []byte
	if state.ControlID != uuid.Nil {
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT ct.agent_profile_id,
			COALESCE(ct.model,''), COALESCE(ct.reasoning_effort,''), COALESCE(ct.service_tier,''),
			p.allowed_tools, '[]'::jsonb FROM codex_thread_controls ct
			JOIN agent_profiles p ON p.id = ct.agent_profile_id WHERE ct.id = $1`, state.ControlID).
			Scan(&profileID, &result.Model, &result.ReasoningEffort, &result.ServiceTier,
				&allowed, &dangerous)
		if err != nil {
			return result, err
		}
	} else {
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT p.id,
			p.allowed_tools, '[]'::jsonb FROM discord_forums f
			CROSS JOIN LATERAL (SELECT id, allowed_tools FROM agent_profiles
			ORDER BY created_at, id LIMIT 1) p WHERE f.id = $1`, state.ForumID).
			Scan(&profileID, &allowed, &dangerous)
		if err != nil {
			return result, err
		}
	}
	_ = json.Unmarshal(allowed, &result.AllowedTools)
	_ = json.Unmarshal(dangerous, &result.DangerousActions)
	if result.AllowedTools == nil {
		result.AllowedTools = []string{}
	}
	if result.DangerousActions == nil {
		result.DangerousActions = []string{}
	}
	return result, nil
}

func desktopThreadID(response json.RawMessage) (string, error) {
	var value struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(response, &value) != nil || strings.TrimSpace(value.Thread.ID) == "" {
		return "", errors.New("响应缺少 Codex thread.id")
	}
	return value.Thread.ID, nil
}

func desktopRuntimeFromResponse(response json.RawMessage) workerprotocol.DesktopThreadConfig {
	var value struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		ServiceTier     string `json:"serviceTier"`
	}
	_ = json.Unmarshal(response, &value)
	return workerprotocol.DesktopThreadConfig{Model: strings.TrimSpace(value.Model),
		ReasoningEffort: strings.TrimSpace(value.ReasoningEffort),
		ServiceTier:     strings.TrimSpace(value.ServiceTier)}
}

func parseOptionalUUID(value sql.NullString) uuid.UUID {
	if !value.Valid {
		return uuid.Nil
	}
	parsed, _ := uuid.Parse(value.String)
	return parsed
}

func enqueueDesktopThreadFailure(c *gin.Context, tx *sql.Tx, requestID uuid.UUID,
	threadID, messageID, message string,
) error {
	card := discordintegration.ComponentCardPayload{AccentColor: 0xED4245,
		Header: "❌ Codex Desktop · 创建失败",
		Body:   "Codex Thread 未能创建。可以在 Desktop 中重试。",
		Footer: "错误：" + safeDesktopFailure(message)}
	payload, _ := json.Marshal(map[string]any{"channelId": threadID, "messageId": messageID,
		"card": card})
	_, err := tx.ExecContext(c.Request.Context(), `INSERT INTO integration_outbox
		(integration, operation_key, operation_type, route_key, payload)
		VALUES ('discord',$1,'message.update',$2,$3)
		ON CONFLICT(integration, operation_key) DO UPDATE SET payload = EXCLUDED.payload,
			status = CASE WHEN integration_outbox.status = 'sending' THEN 'sending' ELSE 'pending' END,
			updated_at = now()`, "desktop-thread-failure:"+requestID.String(),
		"channels/"+threadID+"/messages/"+messageID, payload)
	return err
}

func safeDesktopFailure(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "`", "'")
	runes := []rune(value)
	if len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return value
}
