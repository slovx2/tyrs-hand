package workerconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func RunControlChannel(ctx context.Context, controlURL, credential string, service *Service) error {
	endpoint := strings.TrimRight(controlURL, "/") + "/worker/v1/config/ws"
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+credential)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var request workerprotocol.ConfigRPCRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			continue
		}
		response := workerprotocol.ConfigRPCResponse{ID: request.ID}
		result, callErr := handleRequest(service, request.Method, request.Params)
		if callErr != nil {
			response.Error = callErr.Error()
		} else {
			response.Result = result
		}
		encoded, _ := json.Marshal(response)
		if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			return err
		}
	}
}

func handleRequest(service *Service, method string, params json.RawMessage) (any, error) {
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
	default:
		return nil, fmt.Errorf("不支持的 Worker 配置方法 %q", method)
	}
}
