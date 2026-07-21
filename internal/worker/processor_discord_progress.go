package worker

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"go.uber.org/zap"
)

type storedDiscordProgressEvent struct {
	method     string
	params     json.RawMessage
	occurredAt time.Time
}

type discordProgressReporter struct {
	mu        sync.Mutex
	processor *Processor
	claimed   *codexcontrol.ClaimedControl
	jobCtx    discordJobContext
	tracker   *discordintegration.ConversationActionTracker
}

func (p *Processor) newDiscordProgressReporter(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	jobCtx discordJobContext,
) *discordProgressReporter {
	events := make([]storedDiscordProgressEvent, 0)
	rows, err := p.db.QueryContext(ctx, `SELECT event_type, payload, occurred_at
		FROM agent_events WHERE run_id = $1 ORDER BY id`, claimed.RunID)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var event storedDiscordProgressEvent
			if rows.Scan(&event.method, &event.params, &event.occurredAt) == nil {
				events = append(events, event)
			}
		}
	} else {
		p.logger.Warn("读取 Discord 历史进度失败", zap.Error(err), zap.String("run_id", claimed.RunID.String()))
	}
	startedAt := time.Now()
	if len(events) > 0 {
		startedAt = events[0].occurredAt
	}
	tracker := discordintegration.NewConversationActionTracker(startedAt)
	for _, event := range events {
		tracker.ApplyEvent(event.method, event.params)
	}
	return &discordProgressReporter{processor: p, claimed: claimed, jobCtx: jobCtx, tracker: tracker}
}

func (r *discordProgressReporter) observeEvent(ctx context.Context, event codex.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.tracker.ApplyEvent(event.Method, event.Params) {
		return
	}
	r.project(ctx, discordintegration.ConversationRunning, "正在处理请求。", 0)
}

func (r *discordProgressReporter) dynamicTool(request codex.ToolCallRequest, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	params := mustDiscordProgressParams(request, namespace, state)
	eventType := "discord/tool/started"
	if state != "running" {
		eventType = "discord/tool/completed"
	}
	_, _ = r.processor.db.ExecContext(ctx, `INSERT INTO agent_events
		(control_id, intent_id, run_id, event_type, external_event_id, payload)
		VALUES ($1,$2,$3,$4,$5,$6)`, r.claimed.ControlID, r.claimed.ID, r.claimed.RunID,
		eventType, request.CallID+":"+state, params)
	if !r.tracker.ApplyDynamicTool(request.CallID, namespace, request.Tool, request.Arguments, state) {
		return
	}
	r.project(ctx, discordintegration.ConversationRunning, "正在处理请求。", 0)
}

func (r *discordProgressReporter) detail(summary string, durationMillis int64) string {
	var duration time.Duration
	if durationMillis > 0 {
		duration = time.Duration(durationMillis) * time.Millisecond
	}
	return r.tracker.Render(summary, duration)
}

func (r *discordProgressReporter) project(ctx context.Context, state discordintegration.ConversationProgress,
	summary string, durationMillis int64,
) {
	r.processor.projectDiscordConversation(ctx, r.jobCtx, state, r.detail(summary, durationMillis))
}

func mustDiscordProgressParams(request codex.ToolCallRequest, namespace, state string) json.RawMessage {
	var arguments any = map[string]any{}
	_ = json.Unmarshal(request.Arguments, &arguments)
	payload, _ := json.Marshal(map[string]any{"item": map[string]any{
		"id": request.CallID, "type": "dynamicToolCall", "namespace": namespace,
		"tool": request.Tool, "arguments": arguments, "status": state,
	}})
	return payload
}
