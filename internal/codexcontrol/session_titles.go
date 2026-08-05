package codexcontrol

import (
	"context"
	"database/sql"
	"strings"

	"github.com/google/uuid"
)

const sessionTitleInputMaxRunes = 2000

// EnqueueSessionTitleTx 只为 Session 的首条用户消息创建标题任务。
// 迁移不会回填任务，因此历史 fallback Session 不会被异步重命名。
func EnqueueSessionTitleTx(ctx context.Context, tx *sql.Tx, sessionID, messageID uuid.UUID,
	message string,
) error {
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) > sessionTitleInputMaxRunes {
		message = string(runes[:sessionTitleInputMaxRunes])
	}
	_, err := tx.ExecContext(ctx, `WITH inserted AS (
		INSERT INTO workspace_session_title_tasks(
			session_id,workspace_id,first_message_id,first_message_text,title_revision)
		SELECT session.id,session.workspace_id,$2,$3,session.title_revision
		FROM workspace_sessions session
		WHERE session.id=$1
		  AND NOT EXISTS (
			SELECT 1 FROM session_messages previous
			WHERE previous.session_id=session.id AND previous.message_role='user'
			  AND previous.id<>$2
		  )
		ON CONFLICT(session_id) DO NOTHING
		RETURNING session_id
	)
	UPDATE workspace_sessions session
	SET title_source='generating',updated_at=now()
	FROM inserted WHERE session.id=inserted.session_id
	  AND session.title_source='fallback'`, sessionID, messageID, message)
	return err
}
