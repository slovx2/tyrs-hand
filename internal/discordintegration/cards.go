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

type EmbedPayload struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Footer      string              `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
	Fields      []EmbedFieldPayload `json:"fields,omitempty"`
}

type EmbedFieldPayload struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type ConversationProgress string

const (
	ConversationRunning   ConversationProgress = "running"
	ConversationCompleted ConversationProgress = "completed"
	ConversationBlocked   ConversationProgress = "blocked"
	ConversationCanceled  ConversationProgress = "canceled"
	ConversationFailed    ConversationProgress = "failed"
)

func conversationProgressCard(state ConversationProgress, detail string) EmbedPayload {
	detail = cardText(detail, 4096)
	switch state {
	case ConversationCompleted:
		return EmbedPayload{Title: "✅ Codex · 已完成", Description: detail, Color: cardColorGreen,
			Footer: "完整回复见下一条消息"}
	case ConversationBlocked:
		return EmbedPayload{Title: "⚠️ Codex · 需要处理", Description: detail, Color: cardColorYellow,
			Footer: "处理说明见下一条消息"}
	case ConversationCanceled:
		return EmbedPayload{Title: "⏹️ Codex · 已停止", Description: detail, Color: cardColorGray,
			Footer: "本轮不会再发送回复"}
	case ConversationFailed:
		return EmbedPayload{Title: "❌ Codex · 处理失败", Description: detail, Color: cardColorRed,
			Footer: "后台已记录错误，可稍后重试"}
	default:
		return EmbedPayload{Title: "⚙️ Codex · 处理中", Description: detail, Color: cardColorBlurple,
			Footer: "状态会在此卡片中更新"}
	}
}

func workspaceSelectionCard() EmbedPayload {
	return EmbedPayload{Title: "🧭 Codex · 选择工作区", Color: cardColorBlurple,
		Description: "新会话已创建。请选择使用独立的空白工作区，或选择一个你有权访问的仓库。",
		Footer:      "每个 Discord Post 对应一个独立 Codex 会话"}
}

func workspaceReadyCard(repository bool) EmbedPayload {
	description := "已选择空白工作区，首条消息已经进入队列。"
	if repository {
		description = "已选择仓库工作区，首条消息已经进入队列。"
	}
	return EmbedPayload{Title: "✅ Codex · 工作区已就绪", Description: description,
		Color: cardColorGreen, Footer: "你可以继续在此 Post 中发送消息和附件"}
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

func taskCard(task taskProjection, state string) EmbedPayload {
	label, color := taskStatePresentation(state)
	title := fmt.Sprintf("%s #%d · %s", taskKindLabel(task.Kind),
		task.Number, task.Title)
	fields := []EmbedFieldPayload{{Name: "状态", Value: label, Inline: true}}
	if task.Owner != "" && task.Repository != "" {
		fields = append(fields, EmbedFieldPayload{Name: "仓库", Value: "`" + cardText(task.Owner+"/"+task.Repository, 1000) + "`", Inline: true})
	}
	fields = append(fields, EmbedFieldPayload{Name: "GitHub 状态", Value: "`" + cardText(task.WorkItemState, 1000) + "`", Inline: true})
	return EmbedPayload{Title: cardText(title, 256), Color: color, Fields: fields,
		Footer: "每分钟同步 · 此 Post 只读", Timestamp: time.Now().UTC().Format(time.RFC3339)}
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

func taskStateChangeCard(previous, current string) EmbedPayload {
	label, color := taskStatePresentation(current)
	return EmbedPayload{Title: "任务状态已更新", Color: color,
		Description: fmt.Sprintf("`%s` → **%s**", cardText(previous, 1000), label),
		Footer:      "由 Tyrs Hand 自动同步"}
}

func systemStatusCard(queued, running, blocked, failed, workers, outbox int64, gateway string) EmbedPayload {
	color := cardColorGreen
	state := "🟢 运行正常"
	if workers == 0 || (gateway != "connected" && gateway != "resumed") {
		color, state = cardColorRed, "🔴 服务异常"
	} else if blocked > 0 || failed > 0 || outbox > 0 {
		color, state = cardColorYellow, "🟡 需要关注"
	}
	return EmbedPayload{Title: "Tyrs Hand · 系统状态", Description: state, Color: color,
		Fields: []EmbedFieldPayload{
			{Name: "任务队列", Value: fmt.Sprintf("等待 `%d`\n运行 `%d`", queued, running), Inline: true},
			{Name: "需关注", Value: fmt.Sprintf("阻塞 `%d`\n失败 `%d`", blocked, failed), Inline: true},
			{Name: "运行组件", Value: fmt.Sprintf("Worker `%d`\nGateway `%s`", workers, cardText(gateway, 100)), Inline: true},
			{Name: "消息投递", Value: fmt.Sprintf("Outbox 待处理 `%d`", outbox), Inline: true},
		},
		Footer: "每分钟自动更新", Timestamp: time.Now().UTC().Format(time.RFC3339)}
}

func systemAlertsCard(gatewayStatus string, gatewayError bool, workers, failedOutbox int64) EmbedPayload {
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
		return EmbedPayload{Title: "✅ Tyrs Hand · 系统告警", Description: "当前没有基础设施告警。",
			Color: cardColorGreen, Footer: "每分钟自动检查", Timestamp: time.Now().UTC().Format(time.RFC3339)}
	}
	return EmbedPayload{Title: fmt.Sprintf("🚨 Tyrs Hand · 系统告警 · %d 项", len(alerts)),
		Description: strings.Join(alerts, "\n"), Color: cardColorRed,
		Footer: "请在管理后台查看详情", Timestamp: time.Now().UTC().Format(time.RFC3339)}
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
