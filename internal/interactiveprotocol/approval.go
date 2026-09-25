package interactiveprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const (
	UserInput       = "item/tool/requestUserInput"
	CommandApproval = "item/commandExecution/requestApproval"
	FileApproval    = "item/fileChange/requestApproval"
)

func IsApproval(method string) bool { return method == CommandApproval || method == FileApproval }

func Supported(method string) bool { return method == UserInput || IsApproval(method) }

// JSON-RPC 请求 ID 必须保留字符串/整数类型，null 不能作为可回答的请求。
func ValidRequestID(raw json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return false
	}
	switch id := value.(type) {
	case string:
		return true
	case json.Number:
		_, err := id.Int64()
		return err == nil
	default:
		return false
	}
}

func parseApprovalParams(raw json.RawMessage) (approvalParams, error) {
	var request approvalParams
	if strings.TrimSpace(string(raw)) == "null" {
		return request, errors.New("审批参数必须是对象")
	}
	err := json.Unmarshal(raw, &request)
	return request, err
}

type approvalParams struct {
	Command            string            `json:"command"`
	CWD                string            `json:"cwd"`
	Reason             string            `json:"reason"`
	GrantRoot          string            `json:"grantRoot"`
	AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	Execpolicy         []string          `json:"proposedExecpolicyAmendment"`
	Network            []json.RawMessage `json:"proposedNetworkPolicyAmendments"`
}

// 展示问题沿用现有多端交互卡片；真正保存及回传的仍是原生审批响应。
func Questions(method string, params json.RawMessage) (json.RawMessage, error) {
	if !IsApproval(method) {
		return nil, errors.New("不支持的审批请求")
	}
	request, err := parseApprovalParams(params)
	if err != nil {
		return nil, err
	}
	title, detail := "命令审批", request.Command
	if method == FileApproval {
		title, detail = "文件修改审批", request.GrantRoot
	}
	if request.CWD != "" {
		detail += "\n目录：" + request.CWD
	}
	if request.Reason != "" {
		detail += "\n原因：" + request.Reason
	}
	question := title + "\n" + strings.TrimSpace(detail)
	options := []map[string]string{}
	for _, choice := range []struct{ decision, label, description string }{
		{"accept", "允许本次", "只允许本次操作，不扩大后续授权"},
		{"decline", "拒绝", "拒绝本次操作，模型可以继续说明"},
		{"cancel", "取消回合", "拒绝操作并停止当前回合"},
	} {
		encoded, _ := json.Marshal(map[string]string{"decision": choice.decision})
		if _, err := NormalizeAnswer(method, params, encoded); err == nil {
			options = append(options, map[string]string{"label": choice.label, "description": choice.description})
		}
	}
	if len(options) == 0 {
		return nil, errors.New("原生审批没有可展示的安全决策")
	}
	return json.Marshal([]map[string]any{{"id": "approval", "header": title, "question": question, "options": options}})
}

// Discord/手机的选项回答统一转换为原生响应；自由文本不能扩大授权。
func NormalizeAnswer(method string, params, answer json.RawMessage) (json.RawMessage, error) {
	if !IsApproval(method) {
		return nil, errors.New("不支持的审批请求")
	}
	request, err := parseApprovalParams(params)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Decision json.RawMessage `json:"decision"`
		Answers  map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(answer, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Decision) > 0 && envelope.Answers != nil {
		return nil, errors.New("审批答案格式冲突")
	}
	decision := envelope.Decision
	if envelope.Answers != nil {
		choice := envelope.Answers["approval"].Answers
		if len(envelope.Answers) != 1 || len(choice) != 1 {
			return nil, errors.New("审批必须选择一个明确决策")
		}
		values := map[string]string{"允许本次": "accept", "拒绝": "decline", "取消回合": "cancel"}
		value, ok := values[choice[0]]
		if !ok {
			return nil, errors.New("审批选项无效")
		}
		decision, _ = json.Marshal(value)
	}
	var simple string
	if json.Unmarshal(decision, &simple) == nil {
		switch simple {
		case "accept", "acceptForSession", "decline", "cancel":
		default:
			return nil, errors.New("审批决策无效")
		}
	} else if method != CommandApproval || !validAmendment(request, decision) {
		return nil, errors.New("审批策略必须与原生请求的提案一致")
	}
	if request.AvailableDecisions != nil {
		allowed := false
		for _, available := range request.AvailableDecisions {
			allowed = allowed || equalJSON(available, decision)
		}
		if !allowed {
			return nil, errors.New("审批决策未被原生请求提供")
		}
	}
	return json.Marshal(map[string]json.RawMessage{"decision": decision})
}

func validAmendment(request approvalParams, decision json.RawMessage) bool {
	var values map[string]json.RawMessage
	if json.Unmarshal(decision, &values) != nil || len(values) != 1 {
		return false
	}
	if raw, ok := values["acceptWithExecpolicyAmendment"]; ok {
		expected, _ := json.Marshal(map[string]any{"execpolicy_amendment": request.Execpolicy})
		return len(request.Execpolicy) > 0 && equalJSON(raw, expected)
	}
	if raw, ok := values["applyNetworkPolicyAmendment"]; ok {
		for _, proposal := range request.Network {
			expected, _ := json.Marshal(map[string]json.RawMessage{"network_policy_amendment": proposal})
			if equalJSON(raw, expected) {
				return true
			}
		}
	}
	return false
}

func equalJSON(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func AnswerLabel(answer json.RawMessage) string {
	var value struct {
		Decision any `json:"decision"`
	}
	if json.Unmarshal(answer, &value) != nil {
		return "无效审批答案"
	}
	if text, ok := value.Decision.(string); ok {
		if label, exists := map[string]string{"accept": "允许本次", "acceptForSession": "允许本会话", "decline": "拒绝", "cancel": "取消回合"}[text]; exists {
			return label
		}
	}
	return fmt.Sprintf("已确认策略：%s", answer)
}
