package discordintegration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// 帖子一经建立就固定引擎；重复事件不能因论坛默认值改变而迁移会话。
func resolvePostEngine(ctx context.Context, tx *sql.Tx, forumID uuid.UUID,
	input IncomingMessage,
) (runtimeidentity.Engine, error) {
	var engine runtimeidentity.Engine
	err := tx.QueryRowContext(ctx, `SELECT engine FROM discord_conversations
 WHERE guild_id=$1 AND thread_id=$2`, input.GuildID, input.ThreadID).Scan(&engine)
	if err == nil {
		if input.Engine != "" && input.Engine != engine {
			return "", errors.New("已有帖子不能切换引擎")
		}
		return engine, engine.Validate()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	engine = input.Engine
	if engine == "" {
		err = tx.QueryRowContext(ctx, `SELECT default_engine FROM discord_forums WHERE id=$1`, forumID).Scan(&engine)
		if err != nil {
			return "", err
		}
	}
	return engine, engine.Validate()
}

func forumEngine(ctx context.Context, db *sql.DB, forumID uuid.UUID,
	requested runtimeidentity.Engine,
) (runtimeidentity.Engine, error) {
	if requested != "" {
		return requested, requested.Validate()
	}
	var engine runtimeidentity.Engine
	err := db.QueryRowContext(ctx, `SELECT default_engine FROM discord_forums WHERE id=$1`, forumID).Scan(&engine)
	if err != nil {
		return "", err
	}
	return engine, engine.Validate()
}

func engineDisplayName(engine runtimeidentity.Engine) string {
	switch engine {
	case runtimeidentity.Claude:
		return "Claude"
	case runtimeidentity.Codex:
		return "Codex"
	default:
		// 已删除会话的孤立历史卡片不能猜测归属于哪个引擎。
		return "运行时"
	}
}

func (m *Manager) SetWorkspaceForumEngine(ctx context.Context, forumID uuid.UUID, engine runtimeidentity.Engine) error {
	if err := engine.Validate(); err != nil {
		return err
	}
	result, err := m.db.ExecContext(ctx, `UPDATE discord_forums forum SET default_engine=$2
 FROM worker_workspaces workspace JOIN worker_runtimes runtime ON runtime.worker_id=workspace.worker_id
 WHERE forum.id=$1 AND forum.workspace_id=workspace.id AND forum.forum_type='workspace'
 AND runtime.engine=$2 AND runtime.enabled`, forumID, engine)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("论坛不存在或目标引擎未启用")
	}
	return nil
}
