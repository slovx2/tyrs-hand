package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	interactiveButtonPrefix = "codex-input:"
	interactiveModalPrefix  = "codex-input-other:"
)

type InteractiveQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	Options  []InteractiveOption `json:"options,omitempty"`
	IsSecret bool                `json:"isSecret,omitempty"`
}

type InteractiveOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type InteractiveProjection struct {
	ID        uuid.UUID
	GuildID   string
	ThreadID  string
	MessageID string
	Status    string
	Surface   string
	Questions []InteractiveQuestion
	Draft     map[string]json.RawMessage
}

func ProjectInteractiveRequest(ctx context.Context, db *sql.DB, id uuid.UUID) error {
	request, err := loadInteractiveProjection(ctx, db, id, false)
	if err != nil {
		return err
	}
	card := interactiveCard(request)
	operationType := "message.create"
	payload := map[string]any{"channelId": request.ThreadID, "card": card}
	nonce := "interactive-" + id.String()
	if request.MessageID != "" {
		operationType, nonce = "message.update", ""
		payload["messageId"] = request.MessageID
	}
	return NewSQLoutbox(db).Enqueue(ctx, "interactive:"+id.String(), operationType,
		"channels/"+request.ThreadID+"/messages", payload, nonce)
}

func (m *Manager) AnswerInteractive(ctx context.Context, guildID string, id uuid.UUID,
	questionIndex, optionIndex int, freeText string,
) (ComponentCardPayload, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return ComponentCardPayload{}, err
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadInteractiveProjectionTx(ctx, tx, id, true)
	if err != nil {
		return ComponentCardPayload{}, err
	}
	if request.GuildID != guildID {
		return ComponentCardPayload{}, errors.New("交互请求不属于当前 Discord Server")
	}
	if request.Status != "pending" {
		return interactiveCard(request), tx.Commit()
	}
	if questionIndex < 0 || questionIndex >= len(request.Questions) {
		return ComponentCardPayload{}, errors.New("交互问题序号无效")
	}
	question := request.Questions[questionIndex]
	if question.IsSecret {
		return ComponentCardPayload{}, errors.New("secret 问题只能在 Codex Desktop 回答")
	}
	answer := strings.TrimSpace(freeText)
	if optionIndex >= 0 {
		if optionIndex >= len(question.Options) {
			return ComponentCardPayload{}, errors.New("交互选项序号无效")
		}
		answer = question.Options[optionIndex].Label
	}
	if answer == "" {
		return ComponentCardPayload{}, errors.New("回答不能为空")
	}
	encoded, _ := json.Marshal(map[string][]string{"answers": {answer}})
	request.Draft[question.ID] = encoded
	draft, err := json.Marshal(request.Draft)
	if err != nil {
		return ComponentCardPayload{}, err
	}
	complete := nextInteractiveQuestion(request) < 0
	if complete {
		_, err = tx.ExecContext(ctx, `UPDATE codex_interactive_requests SET
			draft_answers=$2, answer=$2, status='resolved', answer_surface='discord',
			resolved_at=now(), updated_at=now() WHERE id=$1 AND status='pending'`, id, draft)
		request.Status, request.Surface = "resolved", "discord"
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE codex_interactive_requests SET
			draft_answers=$2, updated_at=now() WHERE id=$1 AND status='pending'`, id, draft)
	}
	if err != nil {
		return ComponentCardPayload{}, err
	}
	if err := tx.Commit(); err != nil {
		return ComponentCardPayload{}, err
	}
	return interactiveCard(request), nil
}

func loadInteractiveProjection(ctx context.Context, db *sql.DB, id uuid.UUID,
	lock bool,
) (InteractiveProjection, error) {
	if lock {
		return InteractiveProjection{}, errors.New("读取锁必须在事务内执行")
	}
	return scanInteractiveProjection(db.QueryRowContext(ctx, interactiveProjectionQuery(false), id))
}

func loadInteractiveProjectionTx(ctx context.Context, tx *sql.Tx, id uuid.UUID,
	lock bool,
) (InteractiveProjection, error) {
	return scanInteractiveProjection(tx.QueryRowContext(ctx, interactiveProjectionQuery(lock), id))
}

func interactiveProjectionQuery(lock bool) string {
	query := `SELECT q.id, c.guild_id, c.thread_id, COALESCE(q.discord_message_id,''),
		q.status, COALESCE(q.answer_surface,''), q.questions, q.draft_answers
		FROM codex_interactive_requests q JOIN codex_thread_controls ct ON ct.id=q.control_id
		JOIN discord_conversations c ON c.id=ct.discord_conversation_id WHERE q.id=$1`
	if lock {
		query += " FOR UPDATE OF q"
	}
	return query
}

type rowScanner interface {
	Scan(...any) error
}

func scanInteractiveProjection(row rowScanner) (InteractiveProjection, error) {
	var result InteractiveProjection
	var questions, draft json.RawMessage
	if err := row.Scan(&result.ID, &result.GuildID, &result.ThreadID, &result.MessageID,
		&result.Status, &result.Surface, &questions, &draft); err != nil {
		return InteractiveProjection{}, err
	}
	if err := json.Unmarshal(questions, &result.Questions); err != nil {
		return InteractiveProjection{}, err
	}
	result.Draft = make(map[string]json.RawMessage)
	if len(draft) > 0 {
		if err := json.Unmarshal(draft, &result.Draft); err != nil {
			return InteractiveProjection{}, err
		}
	}
	return result, nil
}

func interactiveCard(request InteractiveProjection) ComponentCardPayload {
	if request.Status != "pending" {
		source := request.Surface
		switch source {
		case "desktop":
			source = "Codex Desktop"
		case "discord":
			source = "Discord"
		default:
			source = "自动超时"
		}
		return ComponentCardPayload{AccentColor: cardColorGreen,
			Header: "## ✅ Codex · 已收到回答", Body: "回答来源：`" + cardText(source, 64) + "`",
			Footer: "本轮已继续运行 · 旧按钮已失效"}
	}
	index := nextInteractiveQuestion(request)
	if index < 0 {
		index = len(request.Questions) - 1
	}
	question := request.Questions[index]
	header := strings.TrimSpace(question.Header)
	if header == "" {
		header = "需要你的回答"
	}
	body := fmt.Sprintf("**%d / %d · %s**\n%s", index+1, len(request.Questions),
		cardText(header, 128), cardText(question.Question, 3000))
	card := ComponentCardPayload{AccentColor: cardColorYellow,
		Header: "## ❓ Codex · 等待输入", Body: body,
		Footer: "Desktop 与 Discord 同时可答 · 只接受最先提交的一方"}
	if question.IsSecret {
		card.Body += "\n\n🔒 此问题包含敏感信息，请在 Codex Desktop 回答。"
		card.Footer = "Discord 不收集或展示 Secret 回答"
		return card
	}
	for optionIndex, option := range question.Options {
		label := option.Label
		if option.Description != "" {
			card.Body += "\n\n**" + cardText(option.Label, 80) + "** — " + cardText(option.Description, 500)
		}
		card.Buttons = append(card.Buttons, ComponentButtonPayload{Label: label,
			CustomID: interactiveButtonID(request.ID, index, optionIndex), Style: "primary"})
	}
	label := "其他"
	if len(question.Options) == 0 {
		label = "填写答案"
	}
	card.Buttons = append(card.Buttons, ComponentButtonPayload{Label: label,
		CustomID: interactiveButtonID(request.ID, index, -1)})
	return card
}

func nextInteractiveQuestion(request InteractiveProjection) int {
	for index, question := range request.Questions {
		if _, exists := request.Draft[question.ID]; !exists {
			return index
		}
	}
	return -1
}

func interactiveButtonID(id uuid.UUID, question, option int) string {
	return fmt.Sprintf("%s%s:%d:%d", interactiveButtonPrefix, id, question, option)
}

func parseInteractiveButton(value string) (uuid.UUID, int, int, error) {
	if !strings.HasPrefix(value, interactiveButtonPrefix) {
		return uuid.Nil, 0, 0, errors.New("交互按钮前缀无效")
	}
	parts := strings.Split(strings.TrimPrefix(value, interactiveButtonPrefix), ":")
	if len(parts) != 3 {
		return uuid.Nil, 0, 0, errors.New("交互按钮格式无效")
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, 0, 0, err
	}
	var question, option int
	if _, err = fmt.Sscanf(parts[1]+":"+parts[2], "%d:%d", &question, &option); err != nil {
		return uuid.Nil, 0, 0, errors.New("交互按钮序号无效")
	}
	return id, question, option, nil
}
