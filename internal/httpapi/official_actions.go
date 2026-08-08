package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

func (s *Server) processOfficialThreadAction(ctx context.Context,
	client *codex.SocketClient, workspaceID uuid.UUID,
) (bool, error) {
	action, err := officialapp.ClaimNextThreadAction(ctx, s.db, workspaceID, 2*time.Minute)
	if err != nil || action == nil {
		return false, err
	}
	var threadID, lifecycle string
	err = s.db.QueryRowContext(ctx, `SELECT thread_id,lifecycle_state
		FROM official_thread_bindings WHERE workspace_id=$1 AND conversation_id=$2`,
		workspaceID, action.ConversationID).Scan(&threadID, &lifecycle)
	if err != nil {
		return true, s.failOfficialThreadAction(ctx, *action, err)
	}
	switch action.Action {
	case "interrupt":
		thread, readErr := officialapp.ReadThread(ctx, client, threadID)
		if readErr != nil {
			return true, s.failOfficialThreadAction(ctx, *action, readErr)
		}
		active := thread.LatestActiveTurn()
		if active != nil {
			err = client.Call(ctx, "turn/interrupt", map[string]any{
				"threadId": threadID, "turnId": active.ID,
			}, nil)
		}
	case "archive":
		if lifecycle != "archived" {
			thread, readErr := officialapp.ReadThread(ctx, client, threadID)
			if readErr != nil {
				err = readErr
			} else if thread.LatestActiveTurn() != nil {
				return true, officialapp.DeferThreadAction(ctx, s.db, *action, time.Second)
			} else {
				err = client.Call(ctx, "thread/archive", map[string]any{
					"threadId": threadID,
				}, nil)
			}
		}
	case "unarchive":
		if lifecycle != "active" {
			err = client.Call(ctx, "thread/unarchive", map[string]any{
				"threadId": threadID,
			}, nil)
		}
	default:
		err = errors.New("未知官方 Thread action")
	}
	if err != nil {
		var requestErr *codex.RequestError
		if errors.As(err, &requestErr) && requestErr.State == codex.RequestUnknown &&
			(action.Action == "archive" || action.Action == "unarchive") {
			archived, found, checkErr := officialThreadArchiveState(ctx, client, threadID)
			desired := action.Action == "archive"
			if checkErr == nil && found && archived == desired {
				err = nil
			} else if checkErr != nil {
				err = errors.Join(err, checkErr)
			}
		}
	}
	if err != nil {
		return true, s.failOfficialThreadAction(ctx, *action, err)
	}
	if action.Action == "archive" || action.Action == "unarchive" {
		state := "active"
		if action.Action == "archive" {
			state = "archived"
		}
		if err = s.applyOfficialLifecycle(ctx, workspaceID, action.ConversationID,
			threadID, state); err != nil {
			return true, s.failOfficialThreadAction(ctx, *action, err)
		}
	}
	if err = officialapp.CompleteThreadAction(ctx, s.db, *action); err != nil {
		return true, err
	}
	if action.Action == "interrupt" {
		_ = s.syncOfficialThread(ctx, client, workspaceID, threadID)
	}
	return true, nil
}

func (s *Server) failOfficialThreadAction(ctx context.Context,
	action officialapp.ThreadAction, cause error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(cause, err)
	}
	defer func() { _ = tx.Rollback() }()
	final, err := officialapp.FailThreadActionTx(ctx, tx, action, cause)
	if err != nil {
		return errors.Join(cause, err)
	}
	if final && (action.Action == "archive" || action.Action == "unarchive") {
		pending, fallback := "archive_pending", "active"
		if action.Action == "unarchive" {
			pending, fallback = "unarchive_pending", "archived"
		}
		var state string
		err = tx.QueryRowContext(ctx, `SELECT lifecycle_state
			FROM official_thread_bindings WHERE workspace_id=$1 AND conversation_id=$2`,
			action.WorkspaceID, action.ConversationID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			state, err = fallback, nil
		}
		if err != nil {
			return errors.Join(cause, err)
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE discord_conversations SET
			lifecycle_state=$3,lifecycle_revision=lifecycle_revision+1,updated_at=now()
			WHERE id=$1 AND lifecycle_state=$2`, action.ConversationID, pending, state)
		if updateErr != nil {
			return errors.Join(cause, updateErr)
		}
		if count, _ := result.RowsAffected(); count == 1 {
			if err = discordintegration.EnqueueConversationLifecycleTx(ctx, tx,
				action.ConversationID); err != nil {
				return errors.Join(cause, err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func officialThreadArchiveState(ctx context.Context, client *codex.SocketClient,
	threadID string,
) (bool, bool, error) {
	for _, archived := range []bool{false, true} {
		var cursor any
		for {
			var page struct {
				Data       []officialapp.Thread `json:"data"`
				NextCursor *string              `json:"nextCursor"`
			}
			err := client.Call(ctx, "thread/list", map[string]any{
				"archived": archived, "limit": 100, "cursor": cursor,
				"sortKey": "updated_at", "sortDirection": "desc",
			}, &page)
			if err != nil {
				return false, false, err
			}
			for _, thread := range page.Data {
				if thread.ID == threadID {
					return archived, true, nil
				}
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
	}
	return false, false, nil
}

func (s *Server) applyOfficialLifecycle(ctx context.Context, workspaceID,
	conversationID uuid.UUID, threadID, state string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE official_thread_bindings SET
		lifecycle_state=$4,updated_at=now() WHERE workspace_id=$1
		AND conversation_id=$2 AND thread_id=$3`, workspaceID, conversationID, threadID, state)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET lifecycle_state=$2,
		lifecycle_revision=lifecycle_revision+CASE WHEN lifecycle_state<>$2 THEN 1 ELSE 0 END,
		updated_at=now() WHERE id=$1`, conversationID, state)
	if err == nil {
		err = discordintegration.EnqueueConversationLifecycleTx(ctx, tx, conversationID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
