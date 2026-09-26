package interactiveprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
)

type elicitationForm struct {
	Type       string
	Properties map[string]map[string]any
	Required   []string
}

func elicitationQuestions(params json.RawMessage) (json.RawMessage, error) {
	request, _, err := parseElicitation(params)
	if err != nil {
		return nil, err
	}
	message := request.Message
	if request.Mode == "url" {
		message += "\n" + request.URL + "\n请完成网页操作后再确认。"
	}
	questions := []map[string]any{{"id": "mcp:action", "header": "MCP 确认", "question": message,
		"options": []map[string]string{{"label": "确认并继续", "description": "确认 URL 操作，或继续填写表单"},
			{"label": "拒绝", "description": "拒绝此次请求"}, {"label": "取消", "description": "取消此次交互"}}}}
	if request.Mode == "url" {
		return json.Marshal(questions)
	}
	var form elicitationForm
	if err := json.Unmarshal(request.RequestedSchema, &form); err != nil {
		return nil, err
	}
	if form.Type != "object" || form.Properties == nil {
		questions = append(questions, map[string]any{"id": "mcp:content", "header": "表单内容", "question": request.Message + "\n请输入符合以下格式的 JSON：\n" + string(request.RequestedSchema)})
		return json.Marshal(questions)
	}
	for _, name := range sortedFormKeys(form.Properties) {
		field := form.Properties[name]
		title, _ := field["title"].(string)
		if title == "" {
			title = name
		}
		description, _ := field["description"].(string)
		question := title
		if description != "" {
			question += "\n" + description
		}
		if !slices.Contains(form.Required, name) {
			question += "\n选填；不填写时选择或输入“跳过此项”。"
		}
		if kind, _ := field["type"].(string); kind != "string" {
			question += "\n值类型：" + kind
		}
		entry := map[string]any{"id": "mcp:field:" + name, "header": title, "question": question, "isSecret": field["format"] == "password" || field["writeOnly"] == true}
		options := []map[string]string{}
		if field["type"] == "boolean" {
			options = append(options, map[string]string{"label": "true", "description": "是"}, map[string]string{"label": "false", "description": "否"})
		} else {
			for _, choice := range elicitationEnumChoices(field) {
				options = append(options, map[string]string{"label": choice.label, "description": choice.title})
			}
		}
		if len(options) > 3 {
			question += "\n允许值："
			for _, option := range options {
				question += "\n" + option["label"] + "：" + option["description"]
			}
			entry["question"] = question
			options = nil
		}
		if !slices.Contains(form.Required, name) && len(options) < 3 {
			options = append(options, map[string]string{"label": "跳过此项", "description": "不提交此可选字段"})
		}
		if len(options) > 0 {
			entry["options"] = options
		}
		questions = append(questions, entry)
	}
	return json.Marshal(questions)
}

func sortedFormKeys(properties map[string]map[string]any) []string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// 每次 Discord 按钮/表单输入均检查；拒绝和取消立即结束，不要求填写其余字段。
func NormalizeElicitationDraft(params, answer json.RawMessage) (json.RawMessage, bool, error) {
	request, _, err := parseElicitation(params)
	if err != nil {
		return nil, false, err
	}
	var envelope struct {
		Answers map[string]struct{ Answers []string }
	}
	if decodeStrictObject(answer, &envelope) != nil || envelope.Answers == nil {
		return nil, false, errors.New("MCP 草稿格式无效")
	}
	questionsRaw, err := elicitationQuestions(params)
	if err != nil {
		return nil, false, err
	}
	var questions []struct{ ID string }
	if err := json.Unmarshal(questionsRaw, &questions); err != nil {
		return nil, false, err
	}
	allowed := map[string]bool{}
	for _, question := range questions {
		allowed[question.ID] = true
	}
	for key, values := range envelope.Answers {
		if !allowed[key] || len(values.Answers) != 1 {
			return nil, false, errors.New("MCP 草稿包含无效字段")
		}
	}
	choice := envelope.Answers["mcp:action"].Answers
	if len(choice) == 0 {
		return nil, false, nil
	}
	if len(choice) != 1 {
		return nil, false, errors.New("MCP 必须选择一个交互操作")
	}
	action := map[string]string{"确认并继续": "accept", "拒绝": "decline", "取消": "cancel"}[choice[0]]
	if action == "" {
		return nil, false, errors.New("MCP 交互选项无效")
	}
	var content any
	if action == "accept" && request.Mode != "url" {
		var form elicitationForm
		if err := json.Unmarshal(request.RequestedSchema, &form); err != nil {
			return nil, false, err
		}
		if form.Type != "object" || form.Properties == nil {
			values := envelope.Answers["mcp:content"].Answers
			if len(values) == 0 {
				return nil, false, nil
			}
			if len(values) != 1 || json.Unmarshal([]byte(values[0]), &content) != nil {
				return nil, false, errors.New("MCP 表单内容不是合法 JSON")
			}
		} else {
			object := map[string]any{}
			missing := false
			for _, name := range sortedFormKeys(form.Properties) {
				values := envelope.Answers["mcp:field:"+name].Answers
				if len(values) == 0 {
					missing = true
					continue
				}
				if len(values) != 1 {
					return nil, false, errors.New("每个表单字段必须提交一个明确值")
				}
				if values[0] == "跳过此项" && !slices.Contains(form.Required, name) {
					continue
				}
				value, err := elicitationFieldValue(form.Properties[name], values[0])
				if err != nil {
					return nil, false, fmt.Errorf("字段 %s 的类型无效: %w", name, err)
				}
				object[name] = value
			}
			if err := validatePartialElicitation(params, object); err != nil {
				return nil, false, err
			}
			if missing {
				return nil, false, nil
			}
			content = object
		}
	}
	native, err := json.Marshal(map[string]any{"action": action, "content": content, "_meta": nil})
	if err != nil {
		return nil, false, err
	}
	normalized, err := normalizeElicitationAnswer(params, native)
	return normalized, err == nil, err
}

func validatePartialElicitation(params json.RawMessage, values map[string]any) error {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(params, &request); err != nil {
		return err
	}
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(request["requestedSchema"], &definition); err != nil {
		return err
	}
	// 草稿只检查已经填写的字段；整体 required、minProperties 和字段依赖在提交时检查。
	partial := map[string]json.RawMessage{"type": json.RawMessage(`"object"`),
		"properties": definition["properties"], "additionalProperties": json.RawMessage(`false`)}
	for _, key := range []string{"$defs", "definitions"} {
		if value, ok := definition[key]; ok {
			partial[key] = value
		}
	}
	raw, err := json.Marshal(partial)
	if err != nil {
		return err
	}
	request["requestedSchema"] = raw
	raw, err = json.Marshal(request)
	if err != nil {
		return err
	}
	_, schema, err := parseElicitation(raw)
	if err != nil {
		return err
	}
	return schema.Validate(values)
}
