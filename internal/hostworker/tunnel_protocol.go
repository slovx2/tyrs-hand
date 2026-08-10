package hostworker

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const managedBrowserNamespace = "browser_files"

type managedToolRequest struct {
	ID   json.RawMessage
	Call codex.ToolCallRequest
}

type singleConnectionListener struct {
	connection net.Conn
	accepted   chan struct{}
	closed     chan struct{}
	once       sync.Once
}

func newSingleConnectionListener(connection net.Conn) *singleConnectionListener {
	return &singleConnectionListener{connection: connection, accepted: make(chan struct{}),
		closed: make(chan struct{})}
}

func (l *singleConnectionListener) Accept() (net.Conn, error) {
	select {
	case <-l.accepted:
		<-l.closed
		return nil, net.ErrClosed
	default:
		close(l.accepted)
		return l.connection, nil
	}
}

func (l *singleConnectionListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		_ = l.connection.Close()
	})
	return nil
}

func (l *singleConnectionListener) Addr() net.Addr {
	if l.connection == nil {
		return managedTunnelAddress("desktop")
	}
	return l.connection.LocalAddr()
}

type managedTunnelAddress string

func (a managedTunnelAddress) Network() string { return "tyrs-hand" }
func (a managedTunnelAddress) String() string  { return string(a) }

type tunnelMessageWriter interface {
	WriteMessage(int, []byte) error
	EnqueueMessage(int, []byte) bool
}

type queuedTunnelMessage struct {
	messageType int
	payload     []byte
}

type tunnelMessageConnection interface {
	WriteMessage(int, []byte) error
	Close() error
}

type serializedTunnelWriter struct {
	connection tunnelMessageConnection
	mu         sync.Mutex
	queue      chan queuedTunnelMessage
	done       chan struct{}
	closeOnce  sync.Once
}

func newSerializedTunnelWriter(connection tunnelMessageConnection) *serializedTunnelWriter {
	writer := &serializedTunnelWriter{connection: connection,
		queue: make(chan queuedTunnelMessage, 64), done: make(chan struct{})}
	go writer.run()
	return writer
}

func (w *serializedTunnelWriter) WriteMessage(messageType int, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connection.WriteMessage(messageType, payload)
}

func (w *serializedTunnelWriter) EnqueueMessage(messageType int, payload []byte) bool {
	message := queuedTunnelMessage{messageType: messageType,
		payload: append([]byte(nil), payload...)}
	select {
	case <-w.done:
		return false
	case w.queue <- message:
		return true
	default:
		return false
	}
}

func (w *serializedTunnelWriter) Close() {
	w.closeOnce.Do(func() {
		close(w.done)
		_ = w.connection.Close()
	})
}

func (w *serializedTunnelWriter) run() {
	for {
		select {
		case <-w.done:
			return
		case message := <-w.queue:
			if err := w.WriteMessage(message.messageType, message.payload); err != nil {
				_ = w.connection.Close()
				return
			}
		}
	}
}

func (r *Runtime) registerTunnelOutput(output tunnelMessageWriter) uint64 {
	id := r.nextBinding.Add(1)
	r.mu.Lock()
	if r.tunnelOutputs == nil {
		r.tunnelOutputs = make(map[uint64]tunnelMessageWriter)
	}
	r.tunnelOutputs[id] = output
	r.mu.Unlock()
	return id
}

func (r *Runtime) unregisterTunnelOutput(id uint64) {
	r.mu.Lock()
	delete(r.tunnelOutputs, id)
	r.mu.Unlock()
}

func (r *Runtime) broadcastTunnelMessage(originID uint64, messageType int, payload []byte) {
	r.mu.Lock()
	outputs := make([]tunnelMessageWriter, 0, len(r.tunnelOutputs))
	for id, output := range r.tunnelOutputs {
		if id != originID {
			outputs = append(outputs, output)
		}
	}
	r.mu.Unlock()
	for _, output := range outputs {
		output.EnqueueMessage(messageType, payload)
	}
}

func managedMetadataNotification(payload []byte) bool {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(payload, &message) != nil || len(message.ID) > 0 {
		return false
	}
	switch message.Method {
	case "thread/name/updated", "thread/archived", "thread/unarchived",
		"thread/settings/updated":
		return true
	default:
		return false
	}
}

var _ net.Listener = (*singleConnectionListener)(nil)

func rewriteManagedThreadRequest(payload []byte, options RuntimeOptions,
	surface workerprotocol.AppServerTunnelSurface,
) []byte {
	var message struct {
		ID     json.RawMessage `json:"id,omitempty"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	if json.Unmarshal(payload, &message) != nil || message.Params == nil {
		return payload
	}
	if ephemeral, _ := message.Params["ephemeral"].(bool); ephemeral {
		return payload
	}
	switch message.Method {
	case "thread/start":
		message.Params = rewriteManagedParams(message.Params,
			participantidentity.AppendDeveloperInstructions)
		if options.BrowserMCPURL != "" {
			injectManagedBrowserConfig(message.Params, options)
			injectManagedBrowserTool(message.Params, options.BrowserDynamicTool)
		}
	case "thread/fork", "thread/resume":
		message.Params = rewriteManagedParams(message.Params,
			participantidentity.AppendDeveloperInstructions)
		if options.BrowserMCPURL != "" {
			injectManagedBrowserConfig(message.Params, options)
		}
	case "turn/start", "turn/steer":
		if surface == workerprotocol.AppServerTunnelSurfaceControl {
			return payload
		}
		participant := participantidentity.Participant{}
		if options.OwnerParticipant != nil {
			participant = *options.OwnerParticipant
		}
		message.Params = rewriteManagedParams(message.Params, func(params json.RawMessage) json.RawMessage {
			return participantidentity.InjectTurnContext(params, participant)
		})
	default:
		return payload
	}
	result, err := json.Marshal(message)
	if err != nil {
		return payload
	}
	return result
}

func rewriteManagedParams(params map[string]any,
	rewrite func(json.RawMessage) json.RawMessage,
) map[string]any {
	encoded, err := json.Marshal(params)
	if err != nil {
		return params
	}
	var result map[string]any
	if json.Unmarshal(rewrite(encoded), &result) != nil {
		return params
	}
	return result
}

func injectManagedBrowserConfig(params map[string]any, options RuntimeOptions) {
	config, _ := params["config"].(map[string]any)
	if config == nil {
		config = make(map[string]any)
	}
	servers, _ := config["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}
	if _, exists := servers["chrome"]; !exists {
		servers["chrome"] = map[string]any{
			"url":                         options.BrowserMCPURL,
			"bearer_token_env_var":        codex.BrowserMCPWorkerTokenEnvironment,
			"startup_timeout_sec":         10.0,
			"tool_timeout_sec":            120.0,
			"required":                    false,
			"default_tools_approval_mode": "approve",
		}
	}
	config["mcp_servers"] = servers
	policy, _ := config["shell_environment_policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{"inherit": "all"}
	}
	policy["exclude"] = appendUniqueJSONStrings(policy["exclude"],
		codex.BrowserMCPWorkerTokenEnvironment, codex.BrowserMCPDesktopTokenEnvironment)
	config["shell_environment_policy"] = policy
	params["config"] = config
}

func injectManagedBrowserTool(params map[string]any, encoded json.RawMessage) {
	if len(encoded) == 0 || !json.Valid(encoded) {
		return
	}
	current, _ := params["dynamicTools"].([]any)
	for _, entry := range current {
		value, _ := entry.(map[string]any)
		if value["name"] == managedBrowserNamespace {
			return
		}
	}
	var tool any
	if json.Unmarshal(encoded, &tool) == nil {
		params["dynamicTools"] = append(current, tool)
	}
}

func appendUniqueJSONStrings(current any, additions ...string) []string {
	values := make([]string, 0, len(additions)+2)
	switch typed := current.(type) {
	case []any:
		for _, entry := range typed {
			if value, ok := entry.(string); ok {
				values = append(values, value)
			}
		}
	case []string:
		values = append(values, typed...)
	}
	seen := make(map[string]struct{}, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		if _, exists := seen[value]; value == "" || exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func managedBrowserToolRequest(payload []byte) (managedToolRequest, bool) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(payload, &message) != nil || len(message.ID) == 0 ||
		message.Method != "item/tool/call" {
		return managedToolRequest{}, false
	}
	var call codex.ToolCallRequest
	if json.Unmarshal(message.Params, &call) != nil || call.Namespace == nil ||
		*call.Namespace != managedBrowserNamespace {
		return managedToolRequest{}, false
	}
	return managedToolRequest{ID: append(json.RawMessage(nil), message.ID...), Call: call}, true
}

func answerManagedToolRequest(ctx context.Context, upstream *websocket.Conn, writeMu *sync.Mutex,
	request managedToolRequest, handler codex.ToolHandler,
) {
	result, err := handler(ctx, request.Call)
	response := map[string]any{"id": request.ID, "result": result}
	if err != nil {
		response = map[string]any{"id": request.ID,
			"error": map[string]any{"code": -32000, "message": err.Error()}}
	}
	payload, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = upstream.WriteMessage(websocket.TextMessage, payload)
}
