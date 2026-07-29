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
	ID                uuid.UUID
	GuildID           string
	ThreadID          string
	MessageID         string
	AnswerMessageID   string
	Status            string
	Surface           string
	CollaborationMode string
	Questions         []InteractiveQuestion
	Draft             map[string]json.RawMessage
	Answer            json.RawMessage
}

func ProjectInteractiveRequest(ctx context.Context, db *sql.DB, id uuid.UUID) error {
	request, err := loadInteractiveProjection(ctx, db, id, false)
	if err != nil {
		return err
	}
	if request.Status == "resolved" {
		key := "interactive-answer:" + id.String()
		operationType := "message.create"
		payload := map[string]any{"channelId": request.ThreadID, "card": interactiveCard(request)}
		nonce := "interactive-answer-" + id.String()
		if request.AnswerMessageID != "" {
			operationType, nonce = "message.update", ""
			payload["messageId"] = request.AnswerMessageID
		}
		if err := NewSQLoutbox(db).Enqueue(ctx, key, operationType,
			"channels/"+request.ThreadID+"/messages", payload, nonce); err != nil {
			return err
		}
		if request.AnswerMessageID != "" && request.MessageID != "" {
			return enqueueInteractiveAnswerLink(ctx, db, request)
		}
		return nil
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

type InteractiveAnswerResult struct {
	Card     ComponentCardPayload
	Complete bool
}

func (m *Manager) AnswerInteractive(ctx context.Context, guildID string, id uuid.UUID,
	questionIndex, optionIndex int, freeText string,
) (InteractiveAnswerResult, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return InteractiveAnswerResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadInteractiveProjectionTx(ctx, tx, id, true)
	if err != nil {
		return InteractiveAnswerResult{}, err
	}
	if request.GuildID != guildID {
		return InteractiveAnswerResult{}, errors.New("交互请求不属于当前 Discord Server")
	}
	if request.Status != "pending" {
		return InteractiveAnswerResult{Card: interactiveCard(request),
			Complete: request.Status == "resolved"}, tx.Commit()
	}
	if questionIndex < 0 || questionIndex >= len(request.Questions) {
		return InteractiveAnswerResult{}, errors.New("交互问题序号无效")
	}
	question := request.Questions[questionIndex]
	if question.IsSecret {
		return InteractiveAnswerResult{}, errors.New("secret 问题只能在 Codex Desktop 回答")
	}
	answer := strings.TrimSpace(freeText)
	if optionIndex >= 0 {
		if optionIndex >= len(question.Options) {
			return InteractiveAnswerResult{}, errors.New("交互选项序号无效")
		}
		answer = question.Options[optionIndex].Label
	}
	if answer == "" {
		return InteractiveAnswerResult{}, errors.New("回答不能为空")
	}
	encoded, _ := json.Marshal(map[string][]string{"answers": {answer}})
	request.Draft[question.ID] = encoded
	draft, err := json.Marshal(request.Draft)
	if err != nil {
		return InteractiveAnswerResult{}, err
	}
	complete := nextInteractiveQuestion(request) < 0
	if complete {
		finalAnswer, marshalErr := json.Marshal(map[string]any{"answers": request.Draft})
		if marshalErr != nil {
			return InteractiveAnswerResult{}, marshalErr
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_interactive_requests SET
			draft_answers=$2, answer=$3, status='resolved', answer_surface='discord',
			resolved_at=now(), updated_at=now() WHERE id=$1 AND status='pending'`, id, draft,
			finalAnswer)
		request.Status, request.Surface = "resolved", "discord"
		request.Answer = finalAnswer
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE codex_interactive_requests SET
			draft_answers=$2, updated_at=now() WHERE id=$1 AND status='pending'`, id, draft)
	}
	if err != nil {
		return InteractiveAnswerResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InteractiveAnswerResult{}, err
	}
	return InteractiveAnswerResult{Card: interactiveCard(request), Complete: complete}, nil
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
		COALESCE(q.discord_answer_message_id,''),
		q.status, COALESCE(q.answer_surface,''), run.collaboration_mode,
		q.questions, q.draft_answers, COALESCE(q.answer,'null'::jsonb)
		FROM codex_interactive_requests q JOIN codex_thread_controls ct ON ct.id=q.control_id
		JOIN codex_turn_runs run ON run.id=q.run_id
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
	var questions, draft, answer json.RawMessage
	if err := row.Scan(&result.ID, &result.GuildID, &result.ThreadID, &result.MessageID,
		&result.AnswerMessageID,
		&result.Status, &result.Surface, &result.CollaborationMode, &questions, &draft,
		&answer); err != nil {
		return InteractiveProjection{}, err
	}
	result.Answer = answer
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
	if request.Status == "resolved" {
		source := request.Surface
		switch source {
		case "desktop":
			source = "Codex Desktop"
		case "discord":
			source = "Discord"
		default:
			source = "自动超时"
		}
		body := "回答来源：`" + cardText(source, 64) + "`"
		if request.CollaborationMode == "plan" {
			body += " · `模式：Plan`"
		}
		card := ComponentCardPayload{AccentColor: cardColorGreen,
			Header: "✅ Codex · 已收到回答", Body: body}
		for index, question := range request.Questions {
			answer := interactiveQuestionAnswer(request, question)
			content := fmt.Sprintf("**问题 %d · %s**\n%s\n\n**回答**\n%s", index+1,
				cardText(interactiveQuestionTitle(question), 256),
				cardText(question.Question, 0), answer)
			card.Sections = append(card.Sections, splitInteractiveSection(content)...)
		}
		return card
	}
	if request.Status == "expired" {
		return ComponentCardPayload{AccentColor: cardColorGray,
			Header: "⌛ Codex · 回答已超时", Body: "本次交互已自动结束，未创建回答记录。"}
	}
	if request.Status == "interrupted" {
		return ComponentCardPayload{AccentColor: cardColorGray,
			Header: "⏹️ Codex · 交互已中断", Body: "本次交互已结束，未创建回答记录。"}
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
		Header: "❓ Codex · 等待输入", Body: body}
	if request.CollaborationMode == "plan" {
		card.Body = "`模式：Plan`\n\n" + card.Body
	}
	if question.IsSecret {
		card.Body += "\n\n🔒 此问题包含敏感信息，请在 Codex Desktop 回答。"
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

func interactiveQuestionTitle(question InteractiveQuestion) string {
	if title := strings.TrimSpace(question.Header); title != "" {
		return title
	}
	return "需要你的回答"
}

func interactiveQuestionAnswer(request InteractiveProjection, question InteractiveQuestion) string {
	if question.IsSecret {
		return "敏感回答已在 Codex Desktop 提交"
	}
	var envelope struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	_ = json.Unmarshal(request.Answer, &envelope)
	raw := envelope.Answers[question.ID]
	if len(raw) == 0 {
		raw = request.Draft[question.ID]
	}
	var value struct {
		Answers []string `json:"answers"`
	}
	if json.Unmarshal(raw, &value) != nil || len(value.Answers) == 0 {
		return "（未提供回答）"
	}
	answer := cardText(strings.Join(value.Answers, "\n"), 0)
	if answer == "" {
		return "（未提供回答）"
	}
	return answer
}

func splitInteractiveSection(value string) []string {
	runes := []rune(value)
	if len(runes) == 0 {
		return nil
	}
	sections := make([]string, 0, (len(runes)+3999)/4000)
	for len(runes) > 4000 {
		sections = append(sections, string(runes[:4000]))
		runes = runes[4000:]
	}
	if len(runes) > 0 {
		sections = append(sections, string(runes))
	}
	return sections
}

func interactiveAnswerLinkCard(request InteractiveProjection) ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorGreen, Header: "Codex · 已回答问题",
		Buttons: []ComponentButtonPayload{{Label: "查看已回答问题",
			URL: replyJumpURL(request.GuildID, request.ThreadID, request.AnswerMessageID)}}}
}

func interactiveSubmittedCard() ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorYellow,
		Header: "Codex · 回答已提交", Body: "正在整理完整问答…"}
}

func enqueueInteractiveAnswerLink(ctx context.Context, db *sql.DB,
	request InteractiveProjection,
) error {
	return enqueueInteractiveAnswerLinkWith(ctx, request, func(key, operationType, routeKey string,
		payload any, nonce string,
	) error {
		return NewSQLoutbox(db).Enqueue(ctx, key, operationType, routeKey, payload, nonce)
	})
}

func enqueueInteractiveAnswerLinkTx(ctx context.Context, tx *sql.Tx,
	request InteractiveProjection,
) error {
	return enqueueInteractiveAnswerLinkWith(ctx, request, func(key, operationType, routeKey string,
		payload any, nonce string,
	) error {
		return enqueueDiscordOutbox(ctx, tx, key, operationType, routeKey, payload, nonce)
	})
}

func enqueueInteractiveAnswerLinkWith(_ context.Context, request InteractiveProjection,
	enqueue func(string, string, string, any, string) error,
) error {
	payload := map[string]any{"channelId": request.ThreadID, "messageId": request.MessageID,
		"card": interactiveAnswerLinkCard(request)}
	return enqueue("interactive-answer-link:"+request.ID.String(),
		"message.update", "channels/"+request.ThreadID+"/messages/"+request.MessageID, payload, "")
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
