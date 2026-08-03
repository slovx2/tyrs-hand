package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const titleFallbackMaxRunes = 60

type claimedConversationTitle struct {
	ID       uuid.UUID
	ThreadID string
	Body     string
}

// TitleGenerator 只负责投递首条消息的回退标题；最终标题来自宿主 Codex 的
// thread/name/updated 事件。
type TitleGenerator struct{ db *sql.DB }

func NewTitleGenerator(db *sql.DB) *TitleGenerator { return &TitleGenerator{db: db} }

func (g *TitleGenerator) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := g.claim(ctx, "pending")
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, g.schedule(ctx, claimed, fallbackTitle(claimed.Body))
}

func (g *TitleGenerator) RecoverInterrupted(ctx context.Context) error {
	for {
		claimed, err := g.claim(ctx, "generating")
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := g.schedule(ctx, claimed, fallbackTitle(claimed.Body)); err != nil {
			return err
		}
	}
}

func (g *TitleGenerator) claim(ctx context.Context, status string) (claimedConversationTitle, error) {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return claimedConversationTitle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var claimed claimedConversationTitle
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.thread_id,m.body
		FROM discord_conversations c
		JOIN discord_input_messages m ON m.message_id=c.starter_message_id
		WHERE c.title_rename_status=$1
		AND NOT EXISTS (SELECT 1 FROM desktop_thread_requests desktop
			WHERE desktop.conversation_id=c.id)
		ORDER BY c.created_at,c.id FOR UPDATE OF c SKIP LOCKED LIMIT 1`, status).
		Scan(&claimed.ID, &claimed.ThreadID, &claimed.Body)
	if err != nil {
		return claimedConversationTitle{}, err
	}
	if status == "pending" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE discord_conversations
			SET title_rename_status='generating',updated_at=now()
			WHERE id=$1 AND title_rename_status='pending'`, claimed.ID)
		if updateErr != nil {
			return claimedConversationTitle{}, updateErr
		}
		if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
			if countErr != nil {
				return claimedConversationTitle{}, countErr
			}
			return claimedConversationTitle{}, errors.New("discord 标题认领状态已变化")
		}
	}
	return claimed, tx.Commit()
}

func (g *TitleGenerator) schedule(ctx context.Context, claimed claimedConversationTitle,
	title string,
) error {
	title = normalizeConversationTitle(title)
	if title == "" {
		title = fallbackTitle(claimed.Body)
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE discord_conversations
		SET title_rename_status='scheduled',generated_title=$2,updated_at=now()
		WHERE id=$1 AND title_rename_status='generating'`, claimed.ID, title)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		if countErr != nil {
			return countErr
		}
		return errors.New("discord 标题状态已变化")
	}
	payload := map[string]any{"channelId": claimed.ThreadID, "threadName": title,
		"conversationId": claimed.ID.String()}
	if err := enqueueDiscordOutbox(ctx, tx, "conversation-title:"+claimed.ID.String(),
		"thread.rename", "channels/"+claimed.ThreadID, payload, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeConversationTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func fallbackTitle(body string) string {
	body = normalizeConversationTitle(body)
	if body == "" {
		return "Codex 任务"
	}
	return truncateRunes(body, titleFallbackMaxRunes)
}

func truncateRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

func sanitizeLogValue(value string, maxRunes int) string {
	value = normalizeConversationTitle(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
