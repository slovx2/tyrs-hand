package workerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const (
	controlChannelPongWait  = 60 * time.Second
	controlChannelWriteWait = 10 * time.Second
)

// ChannelOptions 描述 Worker 侧控制通道的运行参数。
type ChannelOptions struct {
	ControlURL      string
	Credential      string
	Service         *Service
	ProtocolVersion int
	// Notify 在收到 Control 唤醒通知时回调，必须立即返回，不能阻塞读取循环。
	Notify func(kinds []string)
	// Ready 在能力协商完成后回调；wake 为 true 表示进入事件驱动模式。
	Ready func(wake bool)
}

// RunChannel 维护 Worker 到 Control 的 WebSocket 控制通道。
// 通道同时承载 Control 发起的配置 RPC、Worker 的 hello 协商和唤醒通知。
// 返回后调用方应退避重连，并在重连成功后触发一次全量同步。
func RunChannel(ctx context.Context, options ChannelOptions) error {
	endpoint := strings.TrimRight(options.ControlURL, "/") + "/worker/v1/config/ws"
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+options.Credential)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	// 只有收到 Control 的 ping/pong 后才启用读超时。
	// 旧版 Control 不做保活，此时保持现有行为，避免每 60 秒无谓重连。
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(controlChannelPongWait))
	})
	conn.SetPingHandler(func(appData string) error {
		if err := conn.SetReadDeadline(time.Now().Add(controlChannelPongWait)); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData),
			time.Now().Add(controlChannelWriteWait))
	})
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	helloID := uuid.NewString()
	hello, _ := json.Marshal(workerprotocol.WorkerRPCRequest{
		Type: workerprotocol.MessageTypeRequest, ID: helloID, Method: "hello",
		Params: mustJSON(workerprotocol.WorkerHelloRequest{
			ProtocolVersion: options.ProtocolVersion,
			Capabilities:    []string{workerprotocol.WakeCapability},
		}),
	})
	if err := writeControlMessage(conn, hello); err != nil {
		return err
	}
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var envelope struct {
			Type   string          `json:"type"`
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Kinds  []string        `json:"kinds"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			continue
		}
		if envelope.Type == workerprotocol.MessageTypeNotify {
			if options.Notify != nil {
				options.Notify(envelope.Kinds)
			}
			continue
		}
		if envelope.Type == workerprotocol.MessageTypeResponse && envelope.ID == helloID {
			var response struct {
				Result workerprotocol.WorkerHelloResponse `json:"result"`
			}
			if err := json.Unmarshal(payload, &response); err != nil {
				continue
			}
			if options.Ready != nil {
				options.Ready(hasCapability(response.Result.Capabilities,
					workerprotocol.WakeCapability))
			}
			continue
		}
		if envelope.Type == workerprotocol.MessageTypeResponse {
			continue
		}
		var request workerprotocol.WorkerRPCRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			continue
		}
		if request.ID == "" || request.Method == "" {
			continue
		}
		response := workerprotocol.WorkerRPCResponse{ID: request.ID}
		result, callErr := handleRequest(ctx, options.Service, request.Method, request.Params)
		if callErr != nil {
			response.Error = callErr.Error()
		} else {
			response.Result = result
		}
		encoded, _ := json.Marshal(response)
		if err := writeControlMessage(conn, encoded); err != nil {
			return err
		}
	}
}

func writeControlMessage(conn *websocket.Conn, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(controlChannelWriteWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func hasCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func handleRequest(ctx context.Context, service *Service, method string,
	params json.RawMessage,
) (any, error) {
	switch method {
	case "config.read":
		return service.Read()
	case "config.agents.write":
		var input struct {
			Revision string `json:"revision"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, err
		}
		return service.UpdateAgents(input.Revision, input.Content)
	case "config.provider.write":
		var input struct {
			Revision    string `json:"revision"`
			BaseURL     string `json:"baseUrl"`
			APIKey      string `json:"apiKey"`
			ClearAPIKey bool   `json:"clearApiKey"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, err
		}
		return service.UpdateProvider(input.Revision, input.BaseURL, input.APIKey, input.ClearAPIKey)
	case "oauth.devices.start":
		return service.StartOAuth()
	case "oauth.devices.status":
		return service.OAuthStatus(), nil
	case "oauth.logout":
		return service.OAuthStatus(), service.Logout()
	case "codex.restart":
		return map[string]string{"status": "restart_requested"}, service.Restart()
	case "workspace.projects.scan":
		if service.workspaceRoot == "" || service.workspaceRoot == "." {
			return nil, errors.New("Worker Workspace 根目录未配置")
		}
		scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		projects, scanErr := hostworker.ScanProjects(scanCtx, service.workspaceRoot,
			service.home)
		result := workerprotocol.WorkspaceProjectScanResult{Projects: projects}
		if scanErr != nil {
			result.Projects = nil
			result.ScanError = scanErr.Error()
		}
		return result, nil
	default:
		return nil, fmt.Errorf("不支持的 Worker 配置方法 %q", method)
	}
}
