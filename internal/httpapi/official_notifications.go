package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
)

type officialThreadSettings struct {
	Model             string  `json:"model"`
	Effort            *string `json:"effort"`
	ServiceTier       *string `json:"serviceTier"`
	CollaborationMode struct {
		Mode string `json:"mode"`
	} `json:"collaborationMode"`
}

func (s *Server) handleOfficialMetadataNotification(ctx context.Context,
	workspaceID uuid.UUID, event codex.Event,
) error {
	switch event.Method {
	case "thread/name/updated":
		var value struct {
			ThreadID   string  `json:"threadId"`
			ThreadName *string `json:"threadName"`
		}
		if err := json.Unmarshal(event.Params, &value); err != nil || value.ThreadID == "" {
			return err
		}
		if value.ThreadName == nil {
			return nil
		}
		return s.applyOfficialThreadName(ctx, workspaceID, value.ThreadID, *value.ThreadName)
	case "thread/archived", "thread/unarchived":
		var value struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(event.Params, &value); err != nil || value.ThreadID == "" {
			return err
		}
		state := "active"
		if event.Method == "thread/archived" {
			state = "archived"
		}
		conversationID, err := s.officialConversationID(ctx, workspaceID, value.ThreadID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.applyOfficialLifecycle(ctx, workspaceID, conversationID, value.ThreadID, state)
	case "thread/settings/updated":
		var value struct {
			ThreadID       string                 `json:"threadId"`
			ThreadSettings officialThreadSettings `json:"threadSettings"`
		}
		if err := json.Unmarshal(event.Params, &value); err != nil || value.ThreadID == "" {
			return err
		}
		return s.applyOfficialThreadSettings(ctx, workspaceID, value.ThreadID,
			value.ThreadSettings)
	default:
		return nil
	}
}

func (s *Server) officialConversationID(ctx context.Context, workspaceID uuid.UUID,
	threadID string,
) (uuid.UUID, error) {
	var conversationID uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM official_thread_bindings
		WHERE workspace_id=$1 AND thread_id=$2`, workspaceID, threadID).Scan(&conversationID)
	return conversationID, err
}

func (s *Server) applyOfficialThreadName(ctx context.Context, workspaceID uuid.UUID,
	threadID, threadName string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM official_thread_bindings
		WHERE workspace_id=$1 AND thread_id=$2 FOR UPDATE`, workspaceID, threadID).
		Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err = discordintegration.EnqueueOfficialThreadNameTx(ctx, tx, conversationID,
		threadName); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) applyOfficialThreadSettings(ctx context.Context, workspaceID uuid.UUID,
	threadID string, settings officialThreadSettings,
) error {
	effort, tier := "", ""
	if settings.Effort != nil {
		effort = *settings.Effort
	}
	if settings.ServiceTier != nil {
		tier = *settings.ServiceTier
	}
	return discordintegration.ApplyOfficialThreadSettings(ctx, s.db, workspaceID, threadID,
		discordintegration.OfficialThreadSettings{Model: settings.Model,
			ReasoningEffort: effort, ServiceTier: tier,
			CollaborationMode: settings.CollaborationMode.Mode})
}
