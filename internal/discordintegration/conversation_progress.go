package discordintegration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	conversationLineWidth  = 96
	conversationPageBudget = 3500
)

type conversationActionState string

const (
	actionRunning   conversationActionState = "running"
	actionCompleted conversationActionState = "completed"
	actionFailed    conversationActionState = "failed"
)

type conversationAction struct {
	id         string
	lines      []string
	commentary bool
}

type ConversationTimeline struct {
	Pages    []string
	Updates  int
	Duration time.Duration
}

// ConversationActionTracker 将 Codex item 收敛成适合 Discord 单行展示的行动记录。
type ConversationActionTracker struct {
	mu        sync.Mutex
	startedAt time.Time
	order     []string
	actions   map[string]conversationAction
}

func NewConversationActionTracker(startedAt time.Time) *ConversationActionTracker {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &ConversationActionTracker{startedAt: startedAt, actions: make(map[string]conversationAction)}
}

func (t *ConversationActionTracker) ApplyEvent(method string, params json.RawMessage) bool {
	var payload struct {
		Item   map[string]any `json:"item"`
		ItemID string         `json:"itemId"`
		Delta  string         `json:"delta"`
		Text   string         `json:"text"`
		Phase  string         `json:"phase"`
	}
	if json.Unmarshal(params, &payload) != nil {
		return false
	}
	if method == "item/agentMessage/delta" || method == "item/delta" {
		return t.applyCommentaryDelta(payload.ItemID, payload.Phase, firstNonEmpty(payload.Delta, payload.Text))
	}
	if method != "item/started" && method != "item/completed" &&
		method != "discord/tool/started" && method != "discord/tool/completed" {
		return false
	}
	if payload.Item == nil {
		return false
	}
	if textValue(payload.Item["type"]) == "agentMessage" {
		if textValue(payload.Item["phase"]) != "commentary" {
			return false
		}
		return t.applyCommentary(textValue(payload.Item["id"]), textValue(payload.Item["text"]))
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

func (t *ConversationActionTracker) Timeline(summary string, duration time.Duration) ConversationTimeline {
	t.mu.Lock()
	defer t.mu.Unlock()
	if duration <= 0 {
		duration = time.Since(t.startedAt)
	}
	blocks := make([]string, 0, len(t.order))
	if len(t.order) == 0 {
		if summary = visibleConversationSummary(summary); summary != "" {
			blocks = append(blocks, sanitizeDiscordTimeline(summary))
		}
	} else {
		var toolLines []string
		updates := 0
		flushTools := func() {
			if len(toolLines) > 0 {
				blocks = append(blocks, strings.Join(toolLines, "\n"))
				toolLines = nil
			}
		}
		for _, id := range t.order {
			action := t.actions[id]
			if action.commentary {
				updates++
				flushTools()
				if text := sanitizeDiscordTimeline(firstNonEmpty(action.lines...)); text != "" {
					blocks = append(blocks, text)
				}
				continue
			}
			updates += len(action.lines)
			for _, line := range action.lines {
				toolLines = append(toolLines, "> ↳ "+line)
			}
		}
		flushTools()
		pages := paginateConversationBlocks(blocks, conversationPageBudget)
		return ConversationTimeline{Pages: pages, Updates: updates, Duration: duration}
	}
	pages := paginateConversationBlocks(blocks, conversationPageBudget)
	return ConversationTimeline{Pages: pages, Duration: duration}
}

func (t *ConversationActionTracker) Render(summary string, duration time.Duration) string {
	timeline := t.Timeline(summary, duration)
	result := fmt.Sprintf("`%s` · `%d 项动态`", compactDuration(timeline.Duration), timeline.Updates)
	if len(timeline.Pages) > 0 {
		result += "\n\n" + timeline.Pages[len(timeline.Pages)-1]
	}
	return result
}

func visibleConversationSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	switch summary {
	case "正在处理请求。", "思考中", "Codex 正在处理当前消息。",
		"已接收消息，正在准备工作区。", "本轮处理完成。",
		"本轮已由 Discord 用户主动停止。", "本轮已停止。", "已停止",
		"本轮处理未完成。", "Codex Desktop 正在处理请求。",
		"帖子已创建，消息正在进入长期开发环境队列。":
		return ""
	}
	return summary
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
	lines := formatConversationActions(itemType, item, state)
	if len(lines) == 0 {
		return false
	}
	for index := range lines {
		lines[index] = truncateDisplayLine(lines[index], conversationLineWidth)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.actions[id]; ok && !existing.commentary && slices.Equal(existing.lines, lines) {
		return false
	}
	if _, ok := t.actions[id]; !ok {
		t.order = append(t.order, id)
	}
	t.actions[id] = conversationAction{id: id, lines: lines}
	return true
}

func (t *ConversationActionTracker) applyCommentary(id, text string) bool {
	if id == "" {
		return false
	}
	text = strings.TrimSpace(text)
	t.mu.Lock()
	defer t.mu.Unlock()
	existing, exists := t.actions[id]
	if exists && existing.commentary && slices.Equal(existing.lines, []string{text}) {
		return false
	}
	if !exists {
		t.order = append(t.order, id)
	}
	t.actions[id] = conversationAction{id: id, lines: []string{text}, commentary: true}
	return true
}

func (t *ConversationActionTracker) applyCommentaryDelta(id, phase, delta string) bool {
	if id == "" || delta == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	existing, exists := t.actions[id]
	if phase != "commentary" && (!exists || !existing.commentary) {
		return false
	}
	if !exists {
		t.order = append(t.order, id)
		existing = conversationAction{id: id, lines: []string{""}, commentary: true}
	}
	if len(existing.lines) == 0 {
		existing.lines = []string{""}
	}
	existing.lines[0] += delta
	t.actions[id] = existing
	return true
}

func paginateConversationBlocks(blocks []string, budget int) []string {
	if budget <= 0 {
		budget = conversationPageBudget
	}
	var pages []string
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			pages = append(pages, strings.TrimSpace(current))
			current = ""
		}
	}
	for _, block := range blocks {
		for _, part := range splitConversationBlock(block, budget) {
			separator := ""
			if current != "" {
				separator = "\n\n"
			}
			if len([]rune(current))+len([]rune(separator))+len([]rune(part)) > budget {
				flush()
				separator = ""
			}
			current += separator + part
		}
	}
	flush()
	return pages
}

func splitConversationBlock(value string, budget int) []string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return nil
	}
	parts := make([]string, 0, (len(runes)+budget-1)/budget)
	for len(runes) > budget {
		cut := budget
		for index := budget; index > budget/2; index-- {
			if runes[index-1] == '\n' || unicode.IsSpace(runes[index-1]) {
				cut = index
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if text := strings.TrimSpace(string(runes)); text != "" {
		parts = append(parts, text)
	}
	return parts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sanitizeDiscordTimeline(value string) string {
	value = strings.TrimSpace(value)
	return discordSecretPattern.ReplaceAllString(value, "[已隐藏凭据]")
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

func formatConversationActions(itemType string, item map[string]any,
	state conversationActionState,
) []string {
	switch itemType {
	case "commandExecution":
		return formatCommandActions(item, state)
	case "mcpToolCall", "dynamicToolCall":
		return []string{formatToolAction(item, state)}
	case "collabAgentToolCall":
		return []string{formatCollaborationAction(item, state)}
	case "webSearch":
		return []string{stateLine("搜索网页", quotedTarget(firstText(item, "query", "text")), state)}
	case "fileChange":
		return []string{formatFileChangeAction(item, state)}
	case "imageView":
		return []string{stateLine("查看",
			codeTarget(displayPath(firstText(item, "path", "fileName", "name"))), state)}
	case "imageGeneration":
		return []string{stateLine("生成", "控制台预览图", state)}
	default:
		return nil
	}
}

func formatCommandActions(item map[string]any, state conversationActionState) []string {
	var lines []string
	if actions, ok := item["commandActions"].([]any); ok {
		for _, value := range actions {
			action, ok := value.(map[string]any)
			if !ok {
				continue
			}
			line := formatStructuredCommandAction(action, state)
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	if len(lines) > 0 {
		return lines
	}
	command := displayCommand(textValue(item["command"]))
	if isSearchCommand(command) {
		return []string{stateLine("搜索", codeTarget(command), state)}
	}
	return []string{stateLine("执行", codeTarget(command), state)}
}

func formatStructuredCommandAction(action map[string]any, state conversationActionState) string {
	switch textValue(action["type"]) {
	case "read":
		return stateLine("读取", codeTarget(displayPath(firstText(action, "path", "name"))), state)
	case "search":
		target := quotedTarget(firstText(action, "query", "command", "path"))
		return stateLine("搜索", target, state)
	case "listFiles":
		return stateLine("列出", codeTarget(displayPath(firstText(action, "path", "name"))), state)
	case "unknown":
		return stateLine("执行", codeTarget(displayCommand(firstText(action, "command", "name"))), state)
	default:
		command := displayCommand(firstText(action, "command", "name"))
		if command != "" {
			return stateLine("执行", codeTarget(command), state)
		}
		return ""
	}
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
