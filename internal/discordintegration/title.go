package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

func ScheduleConversationTitle(ctx context.Context, db *sql.DB, conversationID uuid.UUID,
	title string,
) (string, bool, error) {
	title = normalizeConversationTitle(title)
	if title == "" {
		return "", false, errors.New("帖子标题不能为空")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	var threadID string
	err = tx.QueryRowContext(ctx, `UPDATE discord_conversations SET title_rename_status = 'scheduled',
		generated_title = $2, updated_at = now() WHERE id = $1 AND title_rename_status = 'pending'
		RETURNING thread_id`, conversationID, title).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		var existing sql.NullString
		if readErr := tx.QueryRowContext(ctx, `SELECT generated_title FROM discord_conversations WHERE id = $1`,
			conversationID).Scan(&existing); readErr != nil {
			return "", false, readErr
		}
		return existing.String, false, nil
	}
	if err != nil {
		return "", false, err
	}
	payload := map[string]any{"channelId": threadID, "threadName": title,
		"conversationId": conversationID.String()}
	if err := enqueueDiscordOutbox(ctx, tx, "conversation-title:"+conversationID.String(), "thread.rename",
		"channels/"+threadID, payload, ""); err != nil {
		return "", false, err
	}
	return title, true, tx.Commit()
}

func EnsureConversationTitle(ctx context.Context, db *sql.DB, conversationID uuid.UUID,
	fallback string,
) (string, error) {
	var status string
	var title sql.NullString
	err := db.QueryRowContext(ctx, `SELECT title_rename_status, generated_title
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&status, &title)
	if err != nil {
		return "", err
	}
	if title.Valid && title.String != "" {
		return title.String, nil
	}
	if status != "pending" {
		return "", nil
	}
	generated, _, err := ScheduleConversationTitle(ctx, db, conversationID, fallbackTitle(fallback))
	return generated, err
}

func normalizeConversationTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > 100 {
		value = string([]rune(value)[:100])
	}
	return strings.TrimSpace(value)
}

func fallbackTitle(body string) string {
	body = normalizeConversationTitle(body)
	if body == "" {
		return "Codex 开发任务"
	}
	if utf8.RuneCountInString(body) > 60 {
		return string([]rune(body)[:59]) + "…"
	}
	return body
}
