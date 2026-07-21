package discordintegration

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	cardColorBlurple = 0x5865F2
	cardColorGreen   = 0x57F287
	cardColorYellow  = 0xFEE75C
	cardColorRed     = 0xED4245
	cardColorGray    = 0x95A5A6
)

type ComponentCardPayload struct {
	AccentColor int                      `json:"accentColor"`
	Header      string                   `json:"header"`
	Body        string                   `json:"body,omitempty"`
	Timeline    string                   `json:"timeline,omitempty"`
	Footer      string                   `json:"footer,omitempty"`
	Buttons     []ComponentButtonPayload `json:"buttons,omitempty"`
}

type ComponentButtonPayload struct {
	Label    string `json:"label"`
	CustomID string `json:"customId"`
	Style    string `json:"style,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type ConversationProgress string

const (
	ConversationRunning   ConversationProgress = "running"
	ConversationCompleted ConversationProgress = "completed"
	ConversationCanceled  ConversationProgress = "canceled"
	ConversationFailed    ConversationProgress = "failed"
)

func conversationProgressCard(state ConversationProgress, timeline ConversationTimeline,
	page int, runID string,
) ComponentCardPayload {
	header, color, footer := "⚙️ Codex · 处理中", cardColorBlurple,
		"状态会在此卡片中更新 · 不展示工具返回内容"
	switch state {
	case ConversationCompleted:
		header, color, footer = "✅ Codex · 已完成", cardColorGreen,
			"完整回复见下一条消息 · 不展示工具返回内容"
	case ConversationCanceled:
		header, color, footer = "⏹️ Codex · 已停止", cardColorGray,
			"本轮不会再发送回复 · 不展示工具返回内容"
	case ConversationFailed:
		header, color, footer = "❌ Codex · 处理失败", cardColorRed,
			"后台已记录错误，可稍后重试 · 不展示工具返回内容"
	}
	if len(timeline.Pages) == 0 {
		timeline.Pages = []string{"正在处理请求。"}
	}
	page = min(max(page, 0), len(timeline.Pages)-1)
	card := ComponentCardPayload{AccentColor: color, Header: "## " + header,
		Body:     fmt.Sprintf("`%s` · `%d 条更新`", compactDuration(timeline.Duration), timeline.Updates),
		Timeline: timeline.Pages[page], Footer: footer}
	if len(timeline.Pages) > 1 && runID != "" {
		last := len(timeline.Pages) - 1
		card.Footer += fmt.Sprintf(" · 第 %d / %d 页", page+1, len(timeline.Pages))
		card.Buttons = []ComponentButtonPayload{
			{Label: "较早", CustomID: progressButtonID("older", runID, max(0, page-1)), Disabled: page == 0},
			{Label: fmt.Sprintf("%d / %d", page+1, len(timeline.Pages)),
				CustomID: progressButtonID("page", runID, page), Disabled: true},
			{Label: "较新", CustomID: progressButtonID("newer", runID, min(last, page+1)), Disabled: page == last},
			{Label: "最新", CustomID: progressButtonID("latest", runID, last), Disabled: page == last},
		}
	}
	return card
}

func terminatedControlCard() ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorRed, Header: "## ⛔ Codex · 会话已终止",
		Body:   "此会话此前发生了不可恢复错误，当前消息没有进入执行队列。请新建一个 Post 后重试。",
		Footer: "后台已保留错误信息供排查"}
}

func conversationConfigurationCard(model, effort, tier string) ComponentCardPayload {
	if model == "" {
		model = "Codex 默认"
	}
	switch effort {
	case "low":
		effort = "轻"
	case "medium":
		effort = "中"
	case "high":
		effort = "高"
	case "xhigh":
		effort = "极高"
	default:
		effort = "Codex 默认"
	}
	if tier == "fast" {
		tier = "快速"
	} else {
		tier = "标准"
	}
	return ComponentCardPayload{AccentColor: cardColorYellow, Header: "## ⚙️ Codex · 即将启动",
		Body: "可以直接使用后台默认值，或在 20 秒内调整本次会话参数。参数确认后会固定到本会话。\n\n" +
			"**模型**  `" + cardText(model, 128) + "`\n" +
			"**服务等级**  `" + cardText(tier, 32) + "`\n" +
			"**思考等级**  `" + cardText(effort, 32) + "`",
		Footer: "20 秒后自动按以上参数启动"}
}

func taskStatePresentation(state string) (string, int) {
	switch state {
	case "Running":
		return "🔵 进行中", cardColorBlurple
	case "Needs Attention":
		return "🟡 需要处理", cardColorYellow
	case "Completed":
		return "🟢 已完成", cardColorGreen
	case "Failed":
		return "🔴 失败", cardColorRed
	default:
		return "⚪ 待处理", cardColorGray
	}
}

func taskCard(task taskProjection, state string) ComponentCardPayload {
	label, color := taskStatePresentation(state)
	title := fmt.Sprintf("%s #%d · %s", taskKindLabel(task.Kind), task.Number, task.Title)
	body := "**状态**  " + label
	if task.Owner != "" && task.Repository != "" {
		body += "\n**仓库**  `" + cardText(task.Owner+"/"+task.Repository, 1000) + "`"
	}
	body += "\n**GitHub 状态**  `" + cardText(task.WorkItemState, 1000) + "`"
	return ComponentCardPayload{AccentColor: color, Header: "## " + cardText(title, 256), Body: body,
		Footer: "每分钟同步 · 此 Post 只读 · " + time.Now().UTC().Format(time.RFC3339)}
}

func taskKindLabel(kind string) string {
	switch kind {
	case "pull_request":
		return "Pull Request"
	case "issue":
		return "Issue"
	default:
		return cardText(strings.ReplaceAll(kind, "_", " "), 80)
	}
}

func taskStateChangeCard(previous, current string) ComponentCardPayload {
	label, color := taskStatePresentation(current)
	return ComponentCardPayload{AccentColor: color, Header: "## 任务状态已更新",
		Body:   fmt.Sprintf("`%s` → **%s**", cardText(previous, 1000), label),
		Footer: "由 Tyrs Hand 自动同步"}
}

func systemStatusCard(queued, running, failed, workers, outbox int64, gateway string) ComponentCardPayload {
	color, state := cardColorGreen, "🟢 运行正常"
	if workers == 0 || (gateway != "connected" && gateway != "resumed") {
		color, state = cardColorRed, "🔴 服务异常"
	} else if failed > 0 || outbox > 0 {
		color, state = cardColorYellow, "🟡 需要关注"
	}
	body := fmt.Sprintf("%s\n\n**任务队列**  等待 `%d` · 运行 `%d`\n**需关注**  失败 `%d`\n"+
		"**运行组件**  Worker `%d` · Gateway `%s`\n**消息投递**  Outbox 待处理 `%d`",
		state, queued, running, failed, workers, cardText(gateway, 100), outbox)
	return ComponentCardPayload{AccentColor: color, Header: "## Tyrs Hand · 系统状态", Body: body,
		Footer: "每分钟自动更新 · " + time.Now().UTC().Format(time.RFC3339)}
}

func systemAlertsCard(gatewayStatus string, gatewayError bool, workers, failedOutbox int64) ComponentCardPayload {
	alerts := make([]string, 0, 4)
	if gatewayStatus != "connected" && gatewayStatus != "resumed" {
		alerts = append(alerts, "• Gateway：`"+cardText(gatewayStatus, 100)+"`")
	}
	if gatewayError {
		alerts = append(alerts, "• Gateway 最近发生错误，脱敏详情可在管理后台查看。")
	}
	if workers == 0 {
		alerts = append(alerts, "• 当前没有在线 Worker。")
	}
	if failedOutbox > 0 {
		alerts = append(alerts, fmt.Sprintf("• Discord Outbox 有 `%d` 条失败投递。", failedOutbox))
	}
	if len(alerts) == 0 {
		return ComponentCardPayload{AccentColor: cardColorGreen, Header: "## ✅ Tyrs Hand · 系统告警",
			Body: "当前没有基础设施告警。", Footer: "每分钟自动检查 · " + time.Now().UTC().Format(time.RFC3339)}
	}
	return ComponentCardPayload{AccentColor: cardColorRed,
		Header: fmt.Sprintf("## 🚨 Tyrs Hand · 系统告警 · %d 项", len(alerts)),
		Body:   strings.Join(alerts, "\n"), Footer: "请在管理后台查看详情 · " + time.Now().UTC().Format(time.RFC3339)}
}

func cardText(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = discordSecretPattern.ReplaceAllString(value, "[已隐藏凭据]")
	value = discordPathPattern.ReplaceAllString(value, "$1[已隐藏路径]")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
