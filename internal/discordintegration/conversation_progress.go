package discordintegration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	conversationActionLimit = 8
	conversationLineWidth   = 64
)

type conversationActionState string

const (
	actionRunning   conversationActionState = "running"
	actionCompleted conversationActionState = "completed"
	actionFailed    conversationActionState = "failed"
)

type conversationAction struct {
	id   string
	line string
}

// ConversationActionTracker 将 Codex item 收敛成适合 Discord 单行展示的行动记录。
type ConversationActionTracker struct {
	mu        sync.Mutex
	startedAt time.Time
	order     []string
	actions   map[string]conversationAction
	updates   int
}

func NewConversationActionTracker(startedAt time.Time) *ConversationActionTracker {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &ConversationActionTracker{startedAt: startedAt, actions: make(map[string]conversationAction)}
}

func (t *ConversationActionTracker) ApplyEvent(method string, params json.RawMessage) bool {
	if method != "item/started" && method != "item/completed" &&
		method != "discord/tool/started" && method != "discord/tool/completed" {
		return false
	}
	var payload struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(params, &payload) != nil || payload.Item == nil {
		return false
	}
	state := actionRunning
	if strings.HasSuffix(method, "/completed") {
		state = actionCompleted
		if itemFailed(payload.Item) {
			state = actionFailed
		}
	}
	return t.applyItem(payload.Item, state)
}

func (t *ConversationActionTracker) ApplyDynamicTool(callID, namespace, tool string,
	arguments json.RawMessage, state string,
) bool {
	var values map[string]any
	_ = json.Unmarshal(arguments, &values)
	item := map[string]any{
		"id": callID, "type": "dynamicToolCall", "namespace": namespace,
		"tool": tool, "arguments": values, "status": state,
	}
	actionState := actionRunning
	switch state {
	case "completed":
		actionState = actionCompleted
	case "failed":
		actionState = actionFailed
	}
	return t.applyItem(item, actionState)
}

func (t *ConversationActionTracker) Render(summary string, duration time.Duration) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if duration <= 0 {
		duration = time.Since(t.startedAt)
	}
	lines := []string{fmt.Sprintf("`%s` · `%d 条更新`", compactDuration(duration), t.updates), ""}
	if len(t.order) == 0 {
		lines = append(lines, truncateDisplayLine(SanitizeDiscordResult(summary), conversationLineWidth))
		return strings.Join(lines, "\n")
	}
	start := max(0, len(t.order)-conversationActionLimit)
	if start > 0 {
		lines = append(lines, fmt.Sprintf("> *另有 %d 条较早操作已省略*", start))
	}
	for _, id := range t.order[start:] {
		lines = append(lines, "• "+t.actions[id].line)
	}
	return strings.Join(lines, "\n")
}

func (t *ConversationActionTracker) applyItem(item map[string]any, state conversationActionState) bool {
	itemType := textValue(item["type"])
	if !supportedActionType(itemType) {
		return false
	}
	id := textValue(item["id"])
	if id == "" {
		return false
	}
	line := formatConversationAction(itemType, item, state)
	if line == "" {
		return false
	}
	line = truncateDisplayLine(line, conversationLineWidth)
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.actions[id]; ok && existing.line == line {
		return false
	}
	if _, ok := t.actions[id]; !ok {
		t.order = append(t.order, id)
	}
	t.actions[id] = conversationAction{id: id, line: line}
	t.updates++
	return true
}

func supportedActionType(value string) bool {
	switch value {
	case "commandExecution", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall",
		"webSearch", "fileChange", "imageView", "imageGeneration":
		return true
	default:
		return false
	}
}

func itemFailed(item map[string]any) bool {
	if textValue(item["status"]) == "failed" {
		return true
	}
	exitCode, ok := item["exitCode"].(float64)
	return ok && exitCode != 0
}

func formatConversationAction(itemType string, item map[string]any, state conversationActionState) string {
	switch itemType {
	case "commandExecution":
		return formatCommandAction(item, state)
	case "mcpToolCall", "dynamicToolCall":
		return formatToolAction(item, state)
	case "collabAgentToolCall":
		return formatCollaborationAction(item, state)
	case "webSearch":
		return stateLine("搜索网页", quotedTarget(firstText(item, "query", "text")), state)
	case "fileChange":
		return formatFileChangeAction(item, state)
	case "imageView":
		return stateLine("查看", codeTarget(displayPath(firstText(item, "path", "fileName", "name"))), state)
	case "imageGeneration":
		return stateLine("生成", "控制台预览图", state)
	default:
		return ""
	}
}

func formatCommandAction(item map[string]any, state conversationActionState) string {
	command := displayCommand(textValue(item["command"]))
	if isSearchCommand(command) {
		return stateLine("搜索", codeTarget(command), state)
	}
	if actions, ok := item["commandActions"].([]any); ok && len(actions) > 0 {
		if action, ok := actions[0].(map[string]any); ok && textValue(action["type"]) == "read" {
			target := displayPath(firstText(action, "path", "name"))
			return stateLine("读取", codeTarget(target), state)
		}
	}
	return stateLine("执行", codeTarget(command), state)
}

func formatToolAction(item map[string]any, state conversationActionState) string {
	server := firstText(item, "namespace", "server")
	tool := firstText(item, "tool", "name")
	name := tool
	if server != "" && tool != "" {
		name = server + "." + tool
	}
	target := codeTarget(name)
	if args := formatArguments(item["arguments"]); args != "" {
		target += "（" + args + "）"
	}
	return stateLine("调用", target, state)
}

func formatCollaborationAction(item map[string]any, state conversationActionState) string {
	target := firstText(item, "receiverThreadId", "agent", "taskName", "tool")
	if args, ok := item["arguments"].(map[string]any); ok {
		if value := firstText(args, "task_name", "target", "receiver"); value != "" {
			target = value
		}
	}
	return stateLine("委派", codeTarget(target), state)
}

func formatFileChangeAction(item map[string]any, state conversationActionState) string {
	target := "文件"
	verb := "修改"
	if changes, ok := item["changes"].([]any); ok && len(changes) > 0 {
		if change, ok := changes[0].(map[string]any); ok {
			target = displayPath(textValue(change["path"]))
			kind, _ := change["kind"].(map[string]any)
			switch textValue(kind["type"]) {
			case "add":
				verb = "创建"
			case "delete":
				verb = "删除"
			}
		}
		if len(changes) > 1 {
			target += fmt.Sprintf(" 等 %d 个文件", len(changes))
		}
	}
	return stateLine(verb, codeTarget(target), state)
}

func stateLine(verb, target string, state conversationActionState) string {
	if target == "" || target == "``" || target == "“”" {
		target = "相关内容"
	}
	switch state {
	case actionRunning:
		return "正在" + verb + " " + target
	case actionFailed:
		return verb + "未成功，正在继续处理：" + target
	default:
		return "已" + verb + " " + target
	}
}

func formatArguments(value any) string {
	values, ok := value.(map[string]any)
	if !ok || len(values) == 0 {
		return ""
	}
	remaining := make([]string, 0, len(values))
	for key := range values {
		if ignoredArgumentKey(key) {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	keys := prioritizeArgumentKeys(remaining)
	parts := make([]string, 0, 3)
	for _, key := range keys {
		if len(parts) == 3 {
			break
		}
		if sensitiveArgumentKey(key) {
			continue
		}
		formatted, ok := scalarArgument(values[key])
		if !ok {
			continue
		}
		parts = append(parts, codeTarget(key+"="+formatted))
	}
	return strings.Join(parts, "，")
}

func prioritizeArgumentKeys(keys []string) []string {
	priority := []string{"query", "path", "file", "repo", "repository", "owner", "number",
		"issue_number", "pull_number", "url", "name", "target", "branch", "cmd", "command"}
	byNormalized := make(map[string]string, len(keys))
	for _, key := range keys {
		byNormalized[strings.ToLower(strings.ReplaceAll(key, "-", "_"))] = key
	}
	result := make([]string, 0, len(keys))
	used := make(map[string]bool, len(keys))
	for _, preferred := range priority {
		if key := byNormalized[preferred]; key != "" {
			result = append(result, key)
			used[key] = true
		}
	}
	for _, key := range keys {
		if !used[key] {
			result = append(result, key)
		}
	}
	return result
}

func scalarArgument(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return sanitizeInline(typed), true
	case float64:
		return fmt.Sprintf("%v", typed), true
	case bool:
		return fmt.Sprintf("%t", typed), true
	default:
		return "", false
	}
}

func ignoredArgumentKey(key string) bool {
	normalized := strings.ToLower(key)
	switch normalized {
	case "result", "output", "stdout", "stderr", "aggregatedoutput", "error", "content":
		return true
	default:
		return false
	}
}

func sensitiveArgumentKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{"token", "secret", "password", "passwd", "api_key",
		"authorization", "cookie", "private_key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func firstText(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := textValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func textValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func sanitizeInline(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "`", "'")
	return SanitizeDiscordResult(value)
}

func displayCommand(value string) string {
	value = sanitizeInline(value)
	for _, prefix := range []string{"/bin/zsh -lc ", "zsh -lc ", "/bin/bash -lc ", "bash -lc ", "sh -lc "} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
			break
		}
	}
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
		(value[0] == '"' && value[len(value)-1] == '"')) {
		value = value[1 : len(value)-1]
	}
	return value
}

func isSearchCommand(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "rg ") || strings.HasPrefix(value, "grep ") ||
		strings.HasPrefix(value, "git grep ")
}

func displayPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Base(value)
	}
	return sanitizeInline(value)
}

func codeTarget(value string) string {
	value = sanitizeInline(value)
	if value == "" {
		return ""
	}
	return "`" + value + "`"
}

func quotedTarget(value string) string {
	value = sanitizeInline(value)
	if value == "" {
		return ""
	}
	return "“" + value + "”"
}

func compactDuration(value time.Duration) string {
	seconds := max(1, int(value.Round(time.Second).Seconds()))
	minutes, seconds := seconds/60, seconds%60
	if minutes == 0 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %02ds", minutes, seconds)
}

func truncateDisplayLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if displayLineWidth(value) <= limit {
		return value
	}
	width := 0
	runes := []rune(value)
	for index, current := range runes {
		currentWidth := runeDisplayWidth(current)
		if width+currentWidth+1 > limit {
			truncated := strings.TrimSpace(string(runes[:index]))
			if strings.Count(truncated, "`")%2 != 0 {
				return truncated + "…`"
			}
			return truncated + "…"
		}
		width += currentWidth
	}
	return value
}

func displayLineWidth(value string) int {
	width := 0
	for _, current := range value {
		width += runeDisplayWidth(current)
	}
	return width
}

func runeDisplayWidth(value rune) int {
	if unicode.Is(unicode.Han, value) || unicode.Is(unicode.Hiragana, value) ||
		unicode.Is(unicode.Katakana, value) || unicode.Is(unicode.Hangul, value) || value >= 0xFF01 {
		return 2
	}
	return 1
}
