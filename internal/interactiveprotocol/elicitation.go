package interactiveprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type elicitationRequest struct {
	Mode, Message, URL, ElicitationID, ServerName string
	RequestedSchema                               json.RawMessage
}

type localSchemaOnly struct{}

func (localSchemaOnly) Load(_ string) (any, error) {
	return nil, errors.New("MCP 表单不允许加载外部 schema")
}

func parseElicitation(params json.RawMessage) (elicitationRequest, *jsonschema.Schema, error) {
	var request elicitationRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return request, nil, err
	}
	switch request.Mode {
	case "url":
		target, err := url.Parse(request.URL)
		if err != nil || target.Host == "" || target.User != nil || (target.Scheme != "https" && target.Scheme != "http") || request.ElicitationID == "" {
			return request, nil, errors.New("MCP URL 交互参数无效")
		}
		return request, nil, nil
	case "form", "openai/form":
		var schemaValue any
		if len(request.RequestedSchema) == 0 || json.Unmarshal(request.RequestedSchema, &schemaValue) != nil {
			return request, nil, errors.New("MCP 表单缺少合法 schema")
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.UseLoader(localSchemaOnly{})
		compiler.AssertFormat()
		const location = "https://tyrs-hand.invalid/elicitation.json"
		if err := compiler.AddResource(location, schemaValue); err != nil {
			return request, nil, err
		}
		schema, err := compiler.Compile(location)
		return request, schema, err
	default:
		return request, nil, errors.New("不支持的 MCP 交互模式")
	}
}

func normalizeElicitationAnswer(params, answer json.RawMessage) (json.RawMessage, error) {
	request, schema, err := parseElicitation(params)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(answer, &fields) != nil || fields == nil {
		return nil, errors.New("MCP 回答必须是对象")
	}
	if _, draft := fields["answers"]; draft {
		normalized, complete, err := NormalizeElicitationDraft(params, answer)
		if err == nil && !complete {
			err = errors.New("MCP 表单尚未完整填写")
		}
		return normalized, err
	}
	for key := range fields {
		if key != "action" && key != "content" && key != "_meta" {
			return nil, errors.New("MCP 回答存在未知字段")
		}
	}
	var action string
	if json.Unmarshal(fields["action"], &action) != nil {
		return nil, errors.New("MCP 回答缺少明确 action")
	}
	content := any(nil)
	if raw := fields["content"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &content); err != nil {
			return nil, err
		}
	}
	switch action {
	case "accept":
		if request.Mode == "url" {
			if content != nil {
				return nil, errors.New("URL 确认不能附加表单内容")
			}
		} else if err := schema.Validate(content); err != nil {
			return nil, fmt.Errorf("MCP 表单答案不符合请求 schema: %w", err)
		}
	case "decline", "cancel":
		if content != nil {
			return nil, errors.New("拒绝或取消不能提交表单内容")
		}
	default:
		return nil, errors.New("MCP action 必须是 accept、decline 或 cancel")
	}
	meta := any(nil)
	if raw := fields["_meta"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, err
		}
		if meta != nil {
			if _, ok := meta.(map[string]any); !ok {
				return nil, errors.New("MCP 元数据必须为对象或 null")
			}
		}
	}
	return json.Marshal(map[string]any{"action": action, "content": content, "_meta": meta})
}
