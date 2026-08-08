package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

func (s *Server) bindDiscoveredOfficialThread(ctx context.Context, workspaceID uuid.UUID,
	thread officialapp.Thread, archived bool,
) (bool, error) {
	if thread.ID == "" {
		return false, nil
	}
	lifecycle := "active"
	if archived {
		lifecycle = "archived"
	}
	if thread.ThreadSource != nil && strings.HasPrefix(*thread.ThreadSource,
		"tyrs-hand-discord:") {
		conversationID, err := uuid.Parse(strings.TrimPrefix(*thread.ThreadSource,
			"tyrs-hand-discord:"))
		if err != nil {
			return false, nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer func() { _ = tx.Rollback() }()
		var bindingID uuid.UUID
		err = tx.QueryRowContext(ctx, `INSERT INTO official_thread_bindings(
			workspace_id,conversation_id,workspace_project_id,thread_id,lifecycle_state)
			SELECT $1,conversation.id,conversation.workspace_project_id,$3,$4
			FROM discord_conversations conversation
			JOIN workspace_projects project ON project.id=conversation.workspace_project_id
			WHERE conversation.id=$2 AND project.workspace_id=$1
			ON CONFLICT(workspace_id,thread_id) DO UPDATE SET
				lifecycle_state=EXCLUDED.lifecycle_state,updated_at=now()
			RETURNING id`, workspaceID, conversationID, thread.ID, lifecycle).Scan(&bindingID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET
			lifecycle_state=$2,
			lifecycle_revision=lifecycle_revision+CASE WHEN lifecycle_state<>$2 THEN 1 ELSE 0 END,
			updated_at=now() WHERE id=$1`, conversationID, lifecycle)
		if err == nil {
			err = discordintegration.EnqueueConversationLifecycleTx(ctx, tx, conversationID)
		}
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	projectID, err := s.officialProjectForCWD(ctx, workspaceID, thread.CWD)
	if err != nil || projectID == uuid.Nil {
		return false, err
	}
	var bindingID uuid.UUID
	err = s.db.QueryRowContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,workspace_project_id,thread_id,lifecycle_state)
		VALUES($1,$2,$3,$4)
		ON CONFLICT(workspace_id,thread_id) DO UPDATE SET
			workspace_project_id=COALESCE(official_thread_bindings.workspace_project_id,
				EXCLUDED.workspace_project_id),lifecycle_state=EXCLUDED.lifecycle_state,
			updated_at=now() RETURNING id`, workspaceID, projectID, thread.ID, lifecycle).
		Scan(&bindingID)
	if err != nil {
		return false, err
	}
	if err = s.ensureOfficialThreadDiscordPost(ctx, bindingID, thread); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) officialProjectForCWD(ctx context.Context, workspaceID uuid.UUID,
	cwd string,
) (uuid.UUID, error) {
	var root string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(worker.metadata->'host'->>'workspaceRoot','')
		FROM worker_workspaces workspace JOIN workers worker ON worker.id=workspace.worker_id
		WHERE workspace.id=$1`, workspaceID).Scan(&root); err != nil {
		return uuid.Nil, err
	}
	root = path.Clean(strings.TrimSpace(root))
	cwd = path.Clean(strings.TrimSpace(cwd))
	if root == "." || cwd == "." || !path.IsAbs(root) || !path.IsAbs(cwd) {
		return uuid.Nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,relative_path FROM workspace_projects
		WHERE workspace_id=$1 AND availability_status='available'`, workspaceID)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = rows.Close() }()
	var result uuid.UUID
	matchedLength := -1
	for rows.Next() {
		var id uuid.UUID
		var relative string
		if err = rows.Scan(&id, &relative); err != nil {
			return uuid.Nil, err
		}
		projectPath, pathErr := desktopWorkspacePath(root, relative)
		if pathErr != nil || (cwd != projectPath && !strings.HasPrefix(cwd, projectPath+"/")) {
			continue
		}
		if len(projectPath) > matchedLength {
			result, matchedLength = id, len(projectPath)
		}
	}
	return result, rows.Err()
}

func (s *Server) ensureOfficialThreadDiscordPost(ctx context.Context, bindingID uuid.UUID,
	thread officialapp.Thread,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID uuid.NullUUID
	var forumDiscordID string
	err = tx.QueryRowContext(ctx, `SELECT binding.conversation_id,resource.discord_id
		FROM official_thread_bindings binding
		JOIN discord_forums forum ON forum.workspace_project_id=binding.workspace_project_id
			AND forum.workspace_id=binding.workspace_id AND forum.binding_status='active'
		JOIN discord_resources resource ON resource.id=forum.resource_id AND resource.status='active'
		WHERE binding.id=$1 FOR UPDATE OF binding`, bindingID).
		Scan(&conversationID, &forumDiscordID)
	if errors.Is(err, sql.ErrNoRows) || conversationID.Valid {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	title := strings.TrimSpace(thread.Preview)
	if thread.Name != nil && strings.TrimSpace(*thread.Name) != "" {
		title = strings.TrimSpace(*thread.Name)
	}
	if title == "" {
		title = "Codex Desktop"
	}
	title = normalizeDesktopTitle(title)
	preview := strings.TrimSpace(thread.Preview)
	if preview == "" {
		preview = "已从 Codex Desktop 连接这个官方 Thread。"
	}
	card := discordintegration.DesktopInputCards("Desktop", preview)[0]
	key := "official-thread-post:" + bindingID.String()
	if err = discordintegration.EnqueueTx(ctx, tx, key, "forum.post.create",
		"channels/"+forumDiscordID+"/threads", map[string]any{
			"channelId": forumDiscordID, "threadName": title, "card": card,
		}, "official-thread-"+bindingID.String()); err != nil {
		return err
	}
	return tx.Commit()
}
