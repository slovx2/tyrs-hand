//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 会话标题任务：结构化输出工具调用即结束，只有一次模型请求。
const channelsTitleRequests = 1

// channelsRelayStep 是一个入口的脚本化模型步骤：首次见到标记时请求真实 Bash，工具结果回来后结束。
type channelsRelayStep struct {
	surface, marker, toolID, line string
	sent, resultSeen, resultError bool
	historyComplete               bool
	sessions                      map[string]bool
}

type channelsRelayModel struct {
	mu            sync.Mutex
	path          string
	steps         []*channelsRelayStep
	claude        int
	codex         int
	titleCalls    int
	stepCalls     map[string]int
	sessions      []string
	titleSessions map[string]bool
	requests      []json.RawMessage
	unscoped      []string
	sessionHint   string
}

func (m *channelsRelayModel) respond(t *testing.T, w http.ResponseWriter, req *http.Request, body []byte) {
	var payload struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		Tools    []struct{ Name string }
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Error(err)
		return
	}
	session := req.Header.Get("X-Claude-Code-Session-Id")
	source := "header"
	if session == "" {
		session, source = channelsSessionFromMetadata(payload.Metadata.UserID), "metadata"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claude++
	m.requests = append(m.requests, body)
	if session != "" && m.sessionHint == "" {
		m.sessionHint = source
	}
	var latest json.RawMessage
	for index := len(payload.Messages) - 1; index >= 0; index-- {
		if payload.Messages[index].Role == "user" {
			latest = payload.Messages[index].Content
			break
		}
	}
	hasBash, structured := false, false
	for _, tool := range payload.Tools {
		hasBash = hasBash || tool.Name == "Bash"
		structured = structured || tool.Name == "StructuredOutput"
	}
	var blocks []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
	}
	var text string
	if json.Unmarshal(latest, &text) != nil && json.Unmarshal(latest, &blocks) == nil {
		for _, block := range blocks {
			text += block.Text
		}
	}
	// Worker 的会话标题任务在独立一次性 Claude 线程中以结构化输出生成标题，不属于接力回合。
	if structured {
		m.titleCalls++
		if session != "" {
			m.titleSessions[session] = true
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID == "toolu_channels_title" {
				channelsClaudeText(w, m.claude)
				return
			}
		}
		channelsClaudeTool(w, m.claude, "toolu_channels_title", "StructuredOutput", map[string]any{"title": "Channels relay"})
		return
	}
	for index, step := range m.steps {
		if hasBash && !step.sent && strings.Contains(text, step.marker) {
			// 原生会话连续的直接证据：本回合模型上下文必须带着前序入口的输入。
			step.historyComplete = true
			for _, previous := range m.steps[:index] {
				step.historyComplete = step.historyComplete && strings.Contains(string(body), previous.marker) &&
					strings.Contains(string(body), previous.toolID)
			}
			step.sent = true
			m.stepCalls[step.surface]++
			m.recordSession(step, session)
			quoted := "'" + strings.ReplaceAll(m.path, "'", "'\"'\"'") + "'"
			channelsClaudeTool(w, m.claude, step.toolID, "Bash", map[string]any{
				"command": "printf '" + step.line + "\\n' >> " + quoted, "description": "append relay line"})
			return
		}
	}
	for _, step := range m.steps {
		for _, block := range blocks {
			if step.sent && !step.resultSeen && block.Type == "tool_result" && block.ToolUseID == step.toolID {
				step.resultSeen, step.resultError = true, block.IsError
				m.stepCalls[step.surface]++
				m.recordSession(step, session)
				channelsClaudeText(w, m.claude)
				return
			}
		}
	}
	m.unscoped = append(m.unscoped, fmt.Sprintf("请求 %d 未匹配任何接力步骤或标题任务", m.claude))
	channelsClaudeText(w, m.claude)
}

func (m *channelsRelayModel) recordSession(step *channelsRelayStep, session string) {
	if session == "" {
		return
	}
	step.sessions[session] = true
	for _, known := range m.sessions {
		if known == session {
			return
		}
	}
	m.sessions = append(m.sessions, session)
}

func channelsClaudeTool(w http.ResponseWriter, request int, id, name string, input map[string]any) {
	encoded, _ := json.Marshal(input)
	channelsClaudeStart(w, request)
	bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
	bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
		"type": "input_json_delta", "partial_json": string(encoded)}})
	bootstrapClaudeEnd(w, "tool_use")
}

// 每个模型响应使用唯一消息 ID；SDK 按消息 ID 合并流式片段，固定 ID 会让不同回合的回答互相覆盖。
func channelsClaudeStart(w http.ResponseWriter, request int) {
	bootstrapEvent(w, "message_start", map[string]any{"message": map[string]any{
		"id": fmt.Sprintf("msg_channels_%d", request), "type": "message", "role": "assistant", "model": "mock-claude",
		"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
}

func channelsClaudeText(w http.ResponseWriter, request int) {
	channelsClaudeStart(w, request)
	bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "CHANNELS_RELAY_OK"}})
	bootstrapClaudeEnd(w, "end_turn")
}

// Claude CLI 的 metadata.user_id 为 JSON 或旧版 "..._session_<uuid>"；只取会话段，不记录其他身份字段。
func channelsSessionFromMetadata(value string) string {
	var structured struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(value), &structured) == nil && structured.SessionID != "" {
		return structured.SessionID
	}
	if _, session, found := strings.Cut(value, "_session_"); found {
		return session
	}
	return ""
}

type channelsTurnWatcher struct {
	events *codex.EventSubscription
	seen   []string
}

// 等待指定 Turn（或首个不在 known 中的新 Turn）完成；记录此连接观察到的全部 Turn 顺序。
func (w *channelsTurnWatcher) awaitCompleted(t *testing.T, ctx context.Context, turnID string, known map[string]bool) string {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("等待 Turn %q 完成超时，已观察 %v", turnID, w.seen)
		case event, ok := <-w.events.Events():
			if !ok {
				t.Fatal("事件流在终态前关闭")
			}
			if event.Method != "turn/started" && event.Method != "turn/completed" {
				continue
			}
			var params struct {
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Error  any    `json:"error"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			id := params.Turn.ID
			if event.Method == "turn/started" {
				w.seen = append(w.seen, id)
				continue
			}
			if (turnID != "" && id == turnID) || (turnID == "" && !known[id]) {
				require.Equal(t, "completed", params.Turn.Status, "%v", params.Turn.Error)
				return id
			}
		}
	}
}

type channelsThreadTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		Type     string  `json:"type"`
		ClientID *string `json:"clientId"`
		Content  []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"items"`
}

func (turn channelsThreadTurn) userText() string {
	var text strings.Builder
	for _, item := range turn.Items {
		if item.Type != "userMessage" {
			continue
		}
		for _, content := range item.Content {
			text.WriteString(content.Text)
		}
	}
	return text.String()
}

func (turn channelsThreadTurn) userClientID() string {
	for _, item := range turn.Items {
		if item.Type == "userMessage" && item.ClientID != nil {
			return *item.ClientID
		}
	}
	return ""
}

// 真实 Control/PostgreSQL/Redis + Worker/Hub + claude-codex + Claude SDK/CLI；只有模型和 Discord 网络为本地替身。
// Desktop SSH、手机 SSH 与 Discord 依次在同一 Claude 线程执行真实 Bash 副作用。
func TestWorkerControlChannelsRelayRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Second)
	defer cancel()
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	relay := &channelsRelayModel{stepCalls: map[string]int{}, titleSessions: map[string]bool{}}
	for _, surface := range []string{"desktop", "phone", "discord"} {
		upper := strings.ToUpper(surface)
		relay.steps = append(relay.steps, &channelsRelayStep{surface: surface,
			marker: "CHANNELS_RELAY_" + upper + "_" + nonce, toolID: "toolu_channels_" + surface,
			line: "relay-" + surface + "-" + nonce, sessions: map[string]bool{}})
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		if req.URL.Path == "/api/hello" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		switch req.URL.Path {
		case "/v1/messages":
			relay.respond(t, w, req, body)
		case "/v1/responses":
			relay.mu.Lock()
			relay.codex++
			relay.mu.Unlock()
			bootstrapModelText(w, false)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	relay.path = filepath.Join(f.cfg.WorkerWorkspaceRoot, "channels-relay.txt")
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { stopWorker(); <-done; cleanup() }) })
	entry, err := app.Runtimes.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	var unexpected sync.Map
	rejectServerRequests := func(surface string) codex.ServerRequestHandler {
		return func(_ context.Context, request codex.ServerRequest) (any, error) {
			unexpected.Store(surface+":"+request.Method, true)
			return nil, fmt.Errorf("接力验收不应出现服务端请求 %s", request.Method)
		}
	}
	turnInput := func(text string) []map[string]any {
		return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
	}
	desktopStep, phoneStep, discordStep := relay.steps[0], relay.steps[1], relay.steps[2]

	// 1. Desktop：正式 clientInfo 名称，新建线程并执行第一回合。
	desktop, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ClientName: "Codex Desktop", ServerRequestHandler: rejectServerRequests("desktop")})
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, desktop.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access"}, &started))
	thread := started.Thread.ID
	require.NotEmpty(t, thread)
	desktopEvents := &channelsTurnWatcher{events: desktop.Subscribe(codex.ThreadFilter{ThreadID: thread})}
	t.Cleanup(desktopEvents.events.Close)
	var desktopTurn struct{ Turn struct{ ID string } }
	require.NoError(t, desktop.Call(ctx, "turn/start", map[string]any{"threadId": thread,
		"approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"},
		"input": turnInput(desktopStep.marker)}, &desktopTurn))
	turnIDs := []string{desktopEvents.awaitCompleted(t, ctx, desktopTurn.Turn.ID, nil)}
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 1)
	require.Equal(t, desktopStep.line+"\n", channelsReadFile(t, relay.path), "Desktop 回合的真实 Bash 只能追加一次")

	// Desktop 线程登记为 Discord 会话，真实 Outbox 经 Discord REST 替身建帖并绑定。
	var conversationID uuid.UUID
	var discordThreadID string
	discord.deliverUntil(t, ctx, func() bool {
		return f.db.QueryRowContext(ctx, `SELECT c.id, c.thread_id FROM desktop_thread_requests r
			JOIN discord_conversations c ON c.id=r.conversation_id
			WHERE r.external_thread_id=$1 AND r.status='completed'`, thread).Scan(&conversationID, &discordThreadID) == nil
	})

	// 2. 手机：独立 SSH 连接、移动端 clientInfo，按移动端顺序读取并恢复同一线程，看到第一回合后执行第二回合。
	phone, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ClientName: "tyrs_hand_mobile", ServerRequestHandler: rejectServerRequests("phone")})
	var metadata struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, phone.Call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, &metadata))
	require.Equal(t, thread, metadata.Thread.ID)
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		InitialTurnsPage struct {
			Data []channelsThreadTurn `json:"data"`
		} `json:"initialTurnsPage"`
	}
	require.NoError(t, phone.Call(ctx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true,
		"initialTurnsPage": map[string]any{"limit": 20, "sortDirection": "desc", "itemsView": "full"}}, &resumed))
	require.Equal(t, thread, resumed.Thread.ID, "手机端不能恢复出新线程")
	require.Len(t, resumed.InitialTurnsPage.Data, 1, "手机端必须看到 Desktop 的第一回合")
	require.Equal(t, turnIDs[0], resumed.InitialTurnsPage.Data[0].ID)
	require.Contains(t, resumed.InitialTurnsPage.Data[0].userText(), desktopStep.marker)
	phoneEvents := &channelsTurnWatcher{events: phone.Subscribe(codex.ThreadFilter{ThreadID: thread})}
	t.Cleanup(phoneEvents.events.Close)
	var phoneTurn struct{ Turn struct{ ID string } }
	require.NoError(t, phone.Call(ctx, "turn/start", map[string]any{"threadId": thread,
		"approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"},
		"input": turnInput(phoneStep.marker)}, &phoneTurn))
	turnIDs = append(turnIDs, phoneEvents.awaitCompleted(t, ctx, phoneTurn.Turn.ID, nil))
	require.Equal(t, turnIDs[1], desktopEvents.awaitCompleted(t, ctx, turnIDs[1], nil), "Desktop 必须实时看到手机回合")
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 2)
	require.Equal(t, desktopStep.line+"\n"+phoneStep.line+"\n", channelsReadFile(t, relay.path))

	// 3. Discord：真实会话服务写入消息与 Control Intent，Worker 认领后在同一 Claude 会话执行。
	discordMessageID := fmt.Sprint(f.discordIDBase + 50000)
	require.NoError(t, discordintegration.NewConversationService(f.db).Reply(ctx, discordintegration.IncomingMessage{
		Engine: runtimeidentity.Claude, GuildID: f.guildID, ThreadID: discordThreadID, MessageID: discordMessageID,
		DiscordUserID: "1001", DisplayName: "owner", Username: "owner", Body: discordStep.marker, MentionsBot: true}))
	app.Runner.NotifyControlWake([]string{"claim"})
	known := map[string]bool{turnIDs[0]: true, turnIDs[1]: true}
	turnIDs = append(turnIDs, desktopEvents.awaitCompleted(t, ctx, "", known))
	require.Equal(t, turnIDs[2], phoneEvents.awaitCompleted(t, ctx, turnIDs[2], nil), "手机也必须看到 Discord 回合")
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 3)
	want := desktopStep.line + "\n" + phoneStep.line + "\n" + discordStep.line + "\n"
	require.Equal(t, want, channelsReadFile(t, relay.path), "三端副作用必须各执行一次且顺序正确")

	// 4. Desktop 读取完整历史：三回合在同一线程且顺序与入口一致。
	var history struct {
		Thread struct {
			ID    string               `json:"id"`
			Turns []channelsThreadTurn `json:"turns"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &history))
	require.Len(t, history.Thread.Turns, 3)
	for index, step := range relay.steps {
		turn := history.Thread.Turns[index]
		require.Equal(t, turnIDs[index], turn.ID)
		require.Equal(t, "completed", turn.Status)
		require.Contains(t, turn.userText(), step.marker, "%s 回合必须出现在 Desktop 历史", step.surface)
	}
	require.Equal(t, discordMessageID, history.Thread.Turns[2].userClientID(), "第三回合必须来自该 Discord 消息")
	require.Equal(t, turnIDs, desktopEvents.seen, "Desktop 事件流必须按序看到三回合")

	// Control 投影：三个 Run 属于同一线程控制，入口按 schema 记录；手机经 SSH 与 Desktop 同为 desktop。
	type projectedRun struct {
		ControlID, ExternalThread, Engine, Surface, Status, TurnID, DiscordMessage, Conversation string
	}
	var runs []projectedRun
	rows, err := f.db.QueryContext(ctx, `SELECT c.id::text, c.external_thread_id, c.engine, COALESCE(i.input_surface,''),
		r.status, COALESCE(r.confirmed_codex_turn_id,''), COALESCE(i.discord_message_id,''),
		COALESCE(c.discord_conversation_id::text,'')
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id=r.primary_intent_id
		JOIN codex_thread_controls c ON c.id=r.control_id WHERE r.worker_id=$1 ORDER BY r.started_at, r.id`, f.workerID)
	require.NoError(t, err)
	for rows.Next() {
		var run projectedRun
		require.NoError(t, rows.Scan(&run.ControlID, &run.ExternalThread, &run.Engine, &run.Surface,
			&run.Status, &run.TurnID, &run.DiscordMessage, &run.Conversation))
		runs = append(runs, run)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, runs, 3, "三个入口恰好三个 Run，不能重复派发")
	for index, run := range runs {
		require.Equal(t, runs[0].ControlID, run.ControlID, "三回合必须属于同一线程控制")
		require.Equal(t, thread, run.ExternalThread)
		require.Equal(t, string(runtimeidentity.Claude), run.Engine)
		require.Equal(t, "completed", run.Status)
		require.Equal(t, turnIDs[index], run.TurnID)
		require.Equal(t, conversationID.String(), run.Conversation)
	}
	surfaces := []string{runs[0].Surface, runs[1].Surface, runs[2].Surface}
	require.Equal(t, []string{"desktop", "desktop", "discord"}, surfaces)
	require.Equal(t, discordMessageID, runs[2].DiscordMessage)
	var controls, codexRuns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls
		WHERE worker_id=$1`, f.workerID).Scan(&controls))
	require.Equal(t, 1, controls, "接力不能分叉出第二个线程控制")
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r
		JOIN codex_thread_controls c ON c.id=r.control_id WHERE c.worker_id=$1 AND c.engine='codex'`, f.workerID).Scan(&codexRuns))
	require.Zero(t, codexRuns)

	// 会话标题是 Worker 的独立异步任务；等它落库后再核对模型请求总数，避免把未完成的标题请求漏计。
	require.Eventually(t, func() bool {
		var title, source string
		err := f.db.QueryRowContext(ctx, `SELECT s.title, s.title_source FROM workspace_sessions s
			JOIN codex_thread_controls c ON c.session_id=s.id WHERE c.id=$1::uuid`, runs[0].ControlID).Scan(&title, &source)
		return err == nil && title == "Channels relay" && source == "generated"
	}, 30*time.Second, 100*time.Millisecond, "会话标题必须由真实 Worker 标题任务生成并写回 Control")
	// Claude 原生会话连续：每个入口的模型请求使用同一会话 ID 并带着前序历史，磁盘上只有这一份接力会话记录。
	relay.mu.Lock()
	defer relay.mu.Unlock()
	t.Logf("模型请求：Claude=%d（接力步骤=%v，会话标题=%d），Codex=%d；会话 ID 来源=%s，接力会话=%v，标题会话数=%d",
		relay.claude, relay.stepCalls, relay.titleCalls, relay.codex, relay.sessionHint, relay.sessions, len(relay.titleSessions))
	require.Empty(t, relay.unscoped)
	for _, step := range relay.steps {
		require.True(t, step.sent, "%s 回合必须由模型发起真实 Bash", step.surface)
		require.True(t, step.resultSeen, "%s 回合的 Bash 结果必须回到模型", step.surface)
		require.False(t, step.resultError, "%s 回合的 Bash 执行失败", step.surface)
		require.True(t, step.historyComplete, "%s 回合的模型上下文必须包含前序入口的输入与工具调用", step.surface)
		require.Equal(t, 2, relay.stepCalls[step.surface], "%s 回合恰好一次工具请求与一次结果请求", step.surface)
	}
	require.Equal(t, 6+relay.titleCalls, relay.claude, "除会话标题任务外不能有额外模型请求")
	require.Equal(t, channelsTitleRequests, relay.titleCalls, "会话标题任务只执行一次")
	require.Equal(t, "header", relay.sessionHint, "必须从 Claude CLI 的会话头取得原生会话 ID")
	require.Len(t, relay.sessions, 1, "三回合必须使用同一 Claude 原生会话，不能分叉")
	session := relay.sessions[0]
	for _, step := range relay.steps {
		require.Equal(t, map[string]bool{session: true}, step.sessions, "%s 回合的全部请求必须属于同一会话", step.surface)
	}
	require.False(t, relay.titleSessions[session], "标题任务不能复用接力会话")
	main := filepath.Base(session + ".jsonl")
	for _, step := range relay.steps[1:] {
		transcripts := channelsTranscripts(t, f.cfg.ClaudeConfigDir(), step.marker)
		require.Len(t, transcripts, 1, "%s 输入只能出现在一份原生会话记录中", step.surface)
		require.Equal(t, main, filepath.Base(transcripts[0]))
	}
	transcripts := channelsTranscripts(t, f.cfg.ClaudeConfigDir(), desktopStep.marker)
	var mainTranscript string
	for _, path := range transcripts {
		if filepath.Base(path) == main {
			mainTranscript = path
			continue
		}
		require.True(t, relay.titleSessions[strings.TrimSuffix(filepath.Base(path), ".jsonl")],
			"首条输入只能额外出现在独立的会话标题记录中：%s", filepath.Base(path))
	}
	require.NotEmpty(t, mainTranscript, "接力会话必须落盘为 %s", main)
	content := channelsReadFile(t, mainTranscript)
	positions := make([]int, 0, len(relay.steps))
	for _, step := range relay.steps {
		require.Equal(t, 1, strings.Count(content, `"id":"`+step.toolID+`"`), "%s 工具调用只能在原生会话出现一次", step.surface)
		positions = append(positions, strings.Index(content, step.marker))
	}
	require.True(t, positions[0] >= 0 && positions[0] < positions[1] && positions[1] < positions[2], "原生会话中的入口顺序错误")
	require.Zero(t, relay.codex, "Codex 引擎不能收到任何模型请求")
	for _, request := range relay.requests {
		require.NotContains(t, string(request), "mock-model", "Codex 配置不能泄漏到 Claude 请求")
	}
	unexpected.Range(func(key, _ any) bool { t.Errorf("出现未预期服务端请求：%v", key); return true })
	saveBootstrapArtifact(t, "models", runtimeidentity.Claude, map[string]any{"requests": relay.requests})
	discord.mu.Lock()
	saveBootstrapArtifact(t, "discord", runtimeidentity.Claude, map[string]any{"deliveries": discord.deliveries})
	discord.mu.Unlock()
	saveBootstrapArtifact(t, "effects", runtimeidentity.Claude, map[string]any{
		"threadId": thread, "turnIds": turnIDs, "inputSurfaces": surfaces,
		"clients":   []string{"Codex Desktop", "tyrs_hand_mobile", "discord"},
		"controlId": runs[0].ControlID, "discordConversationId": conversationID, "discordMessageId": discordMessageID,
		"claudeSessionIds": relay.sessions, "sessionIdSource": relay.sessionHint, "nativeTranscript": main,
		"fileContent": want, "claudeModelRequests": relay.claude, "relayStepRequests": relay.stepCalls,
		"sessionTitleRequests": relay.titleCalls, "codexModelRequests": relay.codex, "codexRuns": codexRuns,
	})
}

func channelsReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

// 返回 Claude 配置目录下包含指定标记的原生会话记录（不含子代理记录）。
func channelsTranscripts(t *testing.T, root, marker string) []string {
	t.Helper()
	var matches []string
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "projects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), marker) {
			matches = append(matches, path)
		}
		return nil
	}))
	return matches
}
