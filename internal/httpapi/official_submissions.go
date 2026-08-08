package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

func (s *Server) processOfficialSubmission(ctx context.Context, client *codex.SocketClient,
	workspaceID uuid.UUID,
) error {
	if err := s.prepareOfficialMaterializations(ctx, workspaceID); err != nil {
		return err
	}
	if err := s.reconcileAmbiguousOfficialSubmissions(ctx, client, workspaceID); err != nil {
		return err
	}
	item, err := officialapp.ClaimNext(ctx, s.db, workspaceID, 2*time.Minute)
	if err != nil || item == nil {
		return err
	}
	threadID, err := s.ensureOfficialConversationThread(ctx, client, *item)
	if err != nil {
		var requestError *codex.RequestError
		if errors.As(err, &requestError) && requestError.State == codex.RequestUnknown {
			return officialapp.MarkAmbiguous(ctx, s.db, *item, err)
		}
		return officialapp.RetryOrFail(ctx, s.db, *item, err)
	}
	item.ThreadID = threadID
	inputs, err := s.officialSubmissionInputs(ctx, *item)
	if err != nil {
		return officialapp.RetryOrFail(ctx, s.db, *item, err)
	}
	if err = s.fillOfficialSubmissionPreferences(ctx, item); err != nil {
		return officialapp.RetryOrFail(ctx, s.db, *item, err)
	}
	result, err := officialapp.Submit(ctx, client, officialapp.SubmitRequest{
		ThreadID: threadID, ClientMessageID: item.ClientMessageID, Input: inputs,
		Preferences: item.Preferences,
		DismissOutstanding: func(dismissCtx context.Context, requestedThreadID string) error {
			return s.dismissOfficialServerRequests(dismissCtx, workspaceID, requestedThreadID)
		},
	})
	if err != nil {
		var requestError *codex.RequestError
		if errors.As(err, &requestError) && requestError.State == codex.RequestUnknown {
			return officialapp.MarkAmbiguous(ctx, s.db, *item, err)
		}
		return officialapp.RetryOrFail(ctx, s.db, *item, err)
	}
	if err = officialapp.Complete(ctx, s.db, *item, result.ThreadID, result.TurnID); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE official_thread_bindings SET
		interactive_owner='control',owned_turn_id=$3,last_client_message_id=$4,updated_at=now()
		WHERE workspace_id=$1 AND thread_id=$2`, workspaceID, result.ThreadID,
		result.TurnID, item.ClientMessageID)
	_, _ = s.db.ExecContext(ctx, `UPDATE discord_input_messages SET status='processed',
		processed_at=now() WHERE official_submission_id=$1`, item.ID)
	return s.syncOfficialThread(ctx, client, workspaceID, result.ThreadID)
}

func (s *Server) fillOfficialSubmissionPreferences(ctx context.Context,
	item *officialapp.Submission,
) error {
	var model, effort, tier, mode sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT model,reasoning_effort,service_tier,
		collaboration_mode FROM discord_conversations WHERE id=$1`, item.ConversationID).
		Scan(&model, &effort, &tier, &mode)
	if err != nil {
		return err
	}
	if item.Preferences.Model == "" {
		item.Preferences.Model = model.String
	}
	if item.Preferences.ReasoningEffort == nil && effort.Valid {
		item.Preferences.ReasoningEffort = &effort.String
	}
	if item.Preferences.ServiceTier == nil && tier.Valid {
		item.Preferences.ServiceTier = &tier.String
	}
	if item.Preferences.CollaborationMode == "" {
		item.Preferences.CollaborationMode = mode.String
	}
	if item.Preferences.CollaborationMode == "" {
		item.Preferences.CollaborationMode = "default"
	}
	return nil
}

func (s *Server) ensureOfficialConversationThread(ctx context.Context,
	client *codex.SocketClient, submission officialapp.Submission,
) (string, error) {
	var threadID string
	err := s.db.QueryRowContext(ctx, `SELECT thread_id FROM official_thread_bindings
		WHERE workspace_id=$1 AND conversation_id=$2`, submission.WorkspaceID,
		submission.ConversationID).Scan(&threadID)
	if err == nil {
		return threadID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var relativePath, workspaceRoot, title string
	err = s.db.QueryRowContext(ctx, `SELECT project.relative_path,
		COALESCE(worker.metadata->'host'->>'workspaceRoot',''),conversation.title
		FROM discord_conversations conversation
		JOIN workspace_projects project ON project.id=conversation.workspace_project_id
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		JOIN workers worker ON worker.id=workspace.worker_id
		WHERE conversation.id=$1 AND workspace.id=$2`, submission.ConversationID,
		submission.WorkspaceID).Scan(&relativePath, &workspaceRoot, &title)
	if err != nil {
		return "", err
	}
	cwd, err := desktopWorkspacePath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}
	source := "tyrs-hand-discord:" + submission.ConversationID.String()
	thread, found, err := findOfficialThreadBySource(ctx, client, source)
	if err != nil {
		return "", err
	}
	if !found {
		params := map[string]any{"cwd": cwd, "runtimeWorkspaceRoots": []string{cwd},
			"threadSource": source}
		if submission.Preferences.Model != "" {
			params["model"] = submission.Preferences.Model
		}
		var response struct {
			Thread officialapp.Thread `json:"thread"`
		}
		if err = client.Call(ctx, "thread/start", params, &response); err != nil {
			if recovered, ok, recoverErr := findOfficialThreadBySource(ctx, client,
				source); recoverErr == nil && ok {
				thread = recovered
			} else {
				return "", errors.Join(err, recoverErr)
			}
		} else {
			thread = response.Thread
		}
	}
	if thread.ID == "" {
		return "", errors.New("官方 thread/start 未返回 Thread ID")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,conversation_id,workspace_project_id,thread_id)
		SELECT $1,$2,workspace_project_id,$3 FROM discord_conversations WHERE id=$2
		ON CONFLICT(conversation_id) DO NOTHING`, submission.WorkspaceID,
		submission.ConversationID, thread.ID)
	if err != nil {
		return "", err
	}
	if title != "" {
		_ = client.Call(ctx, "thread/name/set", map[string]any{
			"threadId": thread.ID, "name": title,
		}, nil)
	}
	thread, err = officialapp.ReadThread(ctx, client, thread.ID)
	if err != nil {
		return "", err
	}
	if err = discordintegration.ProjectOfficialThread(ctx, s.db, submission.WorkspaceID,
		thread); err != nil {
		return "", err
	}
	return thread.ID, nil
}

func findOfficialThreadBySource(ctx context.Context, client *codex.SocketClient,
	source string,
) (officialapp.Thread, bool, error) {
	var cursor any
	for {
		var page struct {
			Data       []officialapp.Thread `json:"data"`
			NextCursor *string              `json:"nextCursor"`
		}
		err := client.Call(ctx, "thread/list", map[string]any{
			"cursor": cursor, "limit": 100, "archived": false,
			"sortKey": "updated_at", "sortDirection": "desc",
		}, &page)
		if err != nil {
			return officialapp.Thread{}, false, err
		}
		for _, thread := range page.Data {
			if thread.ThreadSource != nil && *thread.ThreadSource == source {
				return thread, true, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return officialapp.Thread{}, false, nil
		}
		cursor = *page.NextCursor
	}
}

func (s *Server) reconcileAmbiguousOfficialSubmissions(ctx context.Context,
	client *codex.SocketClient, workspaceID uuid.UUID,
) error {
	var id, conversationID uuid.UUID
	var clientMessageID, threadID string
	var attempt int
	err := s.db.QueryRowContext(ctx, `SELECT submission.id,submission.conversation_id,
		submission.client_user_message_id,
		COALESCE(NULLIF(submission.thread_id,''),binding.thread_id,''),submission.attempt_count
		FROM official_turn_submissions submission
		LEFT JOIN official_thread_bindings binding
			ON binding.conversation_id=submission.conversation_id
		WHERE submission.workspace_id=$1 AND submission.status='ambiguous'
		ORDER BY submission.source_order,submission.id LIMIT 1`, workspaceID).
		Scan(&id, &conversationID, &clientMessageID, &threadID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if threadID == "" {
		source := "tyrs-hand-discord:" + conversationID.String()
		thread, found, findErr := findOfficialThreadBySource(ctx, client, source)
		if findErr != nil || !found {
			return findErr
		}
		threadID = thread.ID
		_, err = s.db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
			workspace_id,conversation_id,workspace_project_id,thread_id)
			SELECT $1,$2,workspace_project_id,$3 FROM discord_conversations WHERE id=$2
			ON CONFLICT(conversation_id) DO NOTHING`, workspaceID, conversationID, threadID)
		if err != nil {
			return err
		}
	}
	thread, err := officialapp.ReadThread(ctx, client, threadID)
	if err != nil {
		return err
	}
	if turn := thread.FindClientMessage(clientMessageID); turn != nil {
		_, err = s.db.ExecContext(ctx, `UPDATE official_turn_submissions SET
			status='submitted',thread_id=$2,turn_id=$3,last_error=NULL,
			submitted_at=now(),updated_at=now() WHERE id=$1 AND status='ambiguous'`, id,
			threadID, turn.ID)
		if err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE discord_input_messages SET
				status='processed',processed_at=now() WHERE official_submission_id=$1`, id)
		}
		if err == nil {
			err = discordintegration.ProjectOfficialThread(ctx, s.db, workspaceID, thread)
		}
		return err
	}
	status := "queued"
	if attempt >= 2 {
		status = "failed"
	}
	_, err = s.db.ExecContext(ctx, `UPDATE official_turn_submissions SET status=$2,
		available_at=now(),last_error=$3,updated_at=now()
		WHERE id=$1 AND status='ambiguous'`, id, status,
		fmt.Sprintf("官方历史中未找到 clientUserMessageId %s", clientMessageID))
	return err
}
