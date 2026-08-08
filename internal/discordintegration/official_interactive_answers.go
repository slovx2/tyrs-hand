package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type OfficialAnswerResult struct {
	Card     ComponentCardPayload
	Complete bool
}

func (m *Manager) AnswerOfficialInput(ctx context.Context, guildID string, id uuid.UUID,
	questionIndex, optionIndex int, freeText string,
) (OfficialAnswerResult, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadOfficialRequest(tx.QueryRowContext(ctx, `SELECT request.id,
		conversation.guild_id,conversation.thread_id,request.method,request.status,
		request.params,request.draft_answers FROM official_server_requests request
		JOIN discord_conversations conversation ON conversation.id=request.conversation_id
		WHERE request.id=$1 FOR UPDATE OF request`, id))
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	if request.GuildID != guildID {
		return OfficialAnswerResult{}, errors.New("官方交互请求不属于当前 Discord Server")
	}
	if request.Status != "pending" {
		return OfficialAnswerResult{Card: officialRequestCard(request), Complete: true}, tx.Commit()
	}
	if request.Method != "item/tool/requestUserInput" {
		return OfficialAnswerResult{}, errors.New("该官方请求不是用户输入请求")
	}
	var params struct {
		Questions []InteractiveQuestion `json:"questions"`
	}
	if err = json.Unmarshal(request.Params, &params); err != nil {
		return OfficialAnswerResult{}, err
	}
	if questionIndex < 0 || questionIndex >= len(params.Questions) {
		return OfficialAnswerResult{}, errors.New("官方交互问题序号无效")
	}
	question := params.Questions[questionIndex]
	if question.IsSecret {
		return OfficialAnswerResult{}, errors.New("敏感问题只能在 Mobile 或 Codex Desktop 回答")
	}
	answer := strings.TrimSpace(freeText)
	if optionIndex >= 0 {
		if optionIndex >= len(question.Options) {
			return OfficialAnswerResult{}, errors.New("官方交互选项序号无效")
		}
		answer = question.Options[optionIndex].Label
	}
	if answer == "" {
		return OfficialAnswerResult{}, errors.New("回答不能为空")
	}
	encodedAnswer, _ := json.Marshal(map[string][]string{"answers": {answer}})
	request.Draft[question.ID] = encodedAnswer
	draft, err := json.Marshal(request.Draft)
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	complete := true
	for _, item := range params.Questions {
		if _, answered := request.Draft[item.ID]; !answered {
			complete = false
			break
		}
	}
	if complete {
		response, marshalErr := json.Marshal(map[string]any{"answers": request.Draft})
		if marshalErr != nil {
			return OfficialAnswerResult{}, marshalErr
		}
		_, err = tx.ExecContext(ctx, `UPDATE official_server_requests SET
			draft_answers=$2,response=$3,status='answered',answer_surface='discord',
			resolved_at=now(),updated_at=now() WHERE id=$1 AND status='pending'`, id,
			draft, response)
		request.Status = "answered"
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE official_server_requests SET
			draft_answers=$2,updated_at=now() WHERE id=$1 AND status='pending'`, id, draft)
	}
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return OfficialAnswerResult{}, err
	}
	return OfficialAnswerResult{Card: officialRequestCard(request), Complete: complete}, nil
}

func (m *Manager) AnswerOfficialApproval(ctx context.Context, guildID string, id uuid.UUID,
	decision string,
) (OfficialAnswerResult, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadOfficialRequest(tx.QueryRowContext(ctx, `SELECT request.id,
		conversation.guild_id,conversation.thread_id,request.method,request.status,
		request.params,request.draft_answers FROM official_server_requests request
		JOIN discord_conversations conversation ON conversation.id=request.conversation_id
		WHERE request.id=$1 FOR UPDATE OF request`, id))
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	if request.GuildID != guildID {
		return OfficialAnswerResult{}, errors.New("官方授权请求不属于当前 Discord Server")
	}
	if request.Status != "pending" {
		return OfficialAnswerResult{Card: officialRequestCard(request), Complete: true}, tx.Commit()
	}
	response, err := officialApprovalResponse(request.Method, decision, request.Params)
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE official_server_requests SET response=$2,
		status='answered',answer_surface='discord',resolved_at=now(),updated_at=now()
		WHERE id=$1 AND status='pending'`, id, response)
	if err != nil {
		return OfficialAnswerResult{}, err
	}
	request.Status = "answered"
	if err = tx.Commit(); err != nil {
		return OfficialAnswerResult{}, err
	}
	return OfficialAnswerResult{Card: officialRequestCard(request), Complete: true}, nil
}

func officialApprovalResponse(method, decision string, params json.RawMessage) (json.RawMessage,
	error,
) {
	if decision != "accept" && decision != "decline" && decision != "cancel" {
		return nil, errors.New("官方授权决定无效")
	}
	var response any
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if decision == "cancel" {
			decision = "decline"
		}
		response = map[string]any{"decision": decision}
	case "item/permissions/requestApproval":
		permissions := map[string]any{}
		if decision == "accept" {
			var value struct {
				Permissions map[string]any `json:"permissions"`
			}
			if err := json.Unmarshal(params, &value); err != nil {
				return nil, err
			}
			for key, item := range value.Permissions {
				if item != nil {
					permissions[key] = item
				}
			}
		}
		response = map[string]any{"permissions": permissions, "scope": "turn"}
	case "mcpServer/elicitation/request":
		if decision == "accept" {
			return nil, errors.New("MCP 输入请求必须在 Mobile 或 Codex Desktop 完成")
		}
		response = map[string]any{"action": decision, "content": nil, "_meta": nil}
	case "applyPatchApproval", "execCommandApproval":
		legacy := any("abort")
		if decision == "accept" {
			legacy = "approved"
		}
		response = map[string]any{"decision": legacy}
	default:
		return nil, errors.New("该官方请求不支持 Discord 授权")
	}
	return json.Marshal(response)
}
