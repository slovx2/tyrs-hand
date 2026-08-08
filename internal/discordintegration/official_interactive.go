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
	officialInputButtonPrefix = "official-input:"
	officialInputModalPrefix  = "official-input-other:"
	officialApprovalPrefix    = "official-approval:"
)

type InteractiveQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	Options  []InteractiveOption `json:"options,omitempty"`
	IsOther  bool                `json:"isOther,omitempty"`
	IsSecret bool                `json:"isSecret,omitempty"`
}

type InteractiveOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type rowScanner interface {
	Scan(...any) error
}

type officialRequestProjection struct {
	ID       uuid.UUID
	GuildID  string
	ThreadID string
	Method   string
	Status   string
	Params   json.RawMessage
	Draft    map[string]json.RawMessage
}

func ProjectOfficialServerRequest(ctx context.Context, db *sql.DB, id uuid.UUID) error {
	request, err := loadOfficialRequest(db.QueryRowContext(ctx, `SELECT request.id,
		conversation.guild_id,conversation.thread_id,request.method,request.status,
		request.params,request.draft_answers FROM official_server_requests request
		JOIN discord_conversations conversation ON conversation.id=request.conversation_id
		WHERE request.id=$1`, id))
	if err != nil {
		return err
	}
	card := officialRequestCard(request)
	key := "official-request:" + id.String()
	var messageID string
	err = db.QueryRowContext(ctx, `INSERT INTO discord_projections(
		guild_id,projection_key,resource_id,desired_payload) VALUES($1,$2,$3,$4)
		ON CONFLICT(guild_id,projection_key) DO UPDATE SET
		desired_payload=EXCLUDED.desired_payload,
		desired_version=discord_projections.desired_version+1,updated_at=now()
		RETURNING COALESCE(message_id,'')`, request.GuildID, key, request.ThreadID,
		mustJSON(map[string]any{"card": card})).Scan(&messageID)
	if err != nil {
		return err
	}
	operation, nonce := "message.create", key
	payload := map[string]any{"channelId": request.ThreadID, "card": card}
	if messageID != "" {
		operation, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	var predecessor sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT 'projection:'||projection_key
		FROM discord_projections WHERE guild_id=$1 AND resource_id=$2
		  AND projection_key LIKE $3
		ORDER BY projection_key DESC LIMIT 1`, request.GuildID, request.ThreadID,
		"official:%").Scan(&predecessor)
	return NewSQLoutbox(db).EnqueueAfter(ctx, "projection:"+key, operation,
		"channels/"+request.ThreadID+"/messages", payload, nonce, predecessor.String)
}

func loadOfficialRequest(row rowScanner) (officialRequestProjection, error) {
	var result officialRequestProjection
	var draft json.RawMessage
	err := row.Scan(&result.ID, &result.GuildID, &result.ThreadID, &result.Method,
		&result.Status, &result.Params, &draft)
	result.Draft = make(map[string]json.RawMessage)
	if err == nil && len(draft) > 0 {
		err = json.Unmarshal(draft, &result.Draft)
	}
	return result, err
}

func officialRequestCard(request officialRequestProjection) ComponentCardPayload {
	if request.Status != "pending" {
		label := "已由其他客户端处理"
		switch request.Status {
		case "answered":
			label = "回答已提交"
		case "dismissed":
			label = "已在发送新消息前跳过"
		case "stale":
			label = "连接已断开，请以官方 Thread 当前状态为准"
		}
		return ComponentCardPayload{AccentColor: cardColorGray,
			Header: "Codex · 交互已结束", Body: label}
	}
	if request.Method == "item/tool/requestUserInput" {
		var params struct {
			Questions []InteractiveQuestion `json:"questions"`
		}
		_ = json.Unmarshal(request.Params, &params)
		questionIndex := -1
		for index, question := range params.Questions {
			if _, answered := request.Draft[question.ID]; !answered {
				questionIndex = index
				break
			}
		}
		if questionIndex < 0 {
			return ComponentCardPayload{AccentColor: cardColorYellow,
				Header: "Codex · 等待提交回答"}
		}
		question := params.Questions[questionIndex]
		card := ComponentCardPayload{AccentColor: cardColorYellow,
			Header: "❓ " + cardText(question.Header, 200),
			Body:   cardText(question.Question, 1800)}
		if question.IsSecret {
			card.Body += "\n\n🔒 请在 Mobile 或 Codex Desktop 中回答这个敏感问题。"
			return card
		}
		for optionIndex, option := range question.Options {
			if option.Description != "" {
				card.Body += "\n\n**" + cardText(option.Label, 80) + "** — " +
					cardText(option.Description, 500)
			}
			card.Buttons = append(card.Buttons, ComponentButtonPayload{
				Label: cardText(option.Label, 80), Style: "secondary",
				CustomID: fmt.Sprintf("%s%s:%d:%d", officialInputButtonPrefix,
					request.ID, questionIndex, optionIndex),
			})
		}
		if question.IsOther {
			card.Buttons = append(card.Buttons, ComponentButtonPayload{Label: "其他…",
				Style: "secondary", CustomID: fmt.Sprintf("%s%s:%d:-1",
					officialInputButtonPrefix, request.ID, questionIndex)})
		}
		return card
	}
	if request.Method == "mcpServer/elicitation/request" {
		var params struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(request.Params, &params)
		return ComponentCardPayload{AccentColor: cardColorYellow,
			Header: "⚠️ Codex · MCP 请求输入",
			Body: cardText(params.Message, 1800) +
				"\n\n完整输入请在 Mobile 或 Codex Desktop 中完成。",
			Buttons: []ComponentButtonPayload{{Label: "取消", Style: "danger",
				CustomID: officialApprovalPrefix + request.ID.String() + ":cancel"}}}
	}
	return ComponentCardPayload{AccentColor: cardColorYellow,
		Header: "⚠️ Codex · 请求授权", Body: cardText(officialApprovalText(request), 1800),
		Buttons: []ComponentButtonPayload{
			{Label: "拒绝", Style: "danger", CustomID: officialApprovalPrefix + request.ID.String() + ":decline"},
			{Label: "允许一次", Style: "primary", CustomID: officialApprovalPrefix + request.ID.String() + ":accept"},
		}}
}

func officialApprovalText(request officialRequestProjection) string {
	var params map[string]any
	_ = json.Unmarshal(request.Params, &params)
	for _, key := range []string{"command", "reason", "grantRoot"} {
		if value, ok := params[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return request.Method
}

func parseOfficialInputButton(value string) (uuid.UUID, int, int, error) {
	parts := strings.Split(strings.TrimPrefix(value, officialInputButtonPrefix), ":")
	if !strings.HasPrefix(value, officialInputButtonPrefix) || len(parts) != 3 {
		return uuid.Nil, 0, 0, errors.New("官方回答按钮无效")
	}
	id, err := uuid.Parse(parts[0])
	var question, option int
	if _, scanErr := fmt.Sscanf(parts[1]+":"+parts[2], "%d:%d", &question, &option); err != nil || scanErr != nil || question < 0 || option < -1 {
		return uuid.Nil, 0, 0, errors.New("官方回答按钮无效")
	}
	return id, question, option, nil
}
