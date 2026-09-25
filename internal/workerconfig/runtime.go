package workerconfig

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// RuntimeRequest 强制携带引擎；缺少或未知引擎不能回落到 Codex。
type RuntimeRequest struct {
	Engine runtimeidentity.Engine `json:"engine"`
	Input  json.RawMessage        `json:"input"`
}

func handleRuntimeRequest(options ChannelOptions, method string, params json.RawMessage) (any, error) {
	var request RuntimeRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	if err := request.Engine.Validate(); err != nil {
		return nil, err
	}
	var input ClaudeProviderInput
	if len(request.Input) > 0 && string(request.Input) != "null" {
		if err := json.Unmarshal(request.Input, &input); err != nil {
			return nil, err
		}
	}
	var instructions struct {
		Revision string `json:"revision"`
		Content  string `json:"content"`
	}
	if method == "config.agents.write" {
		if err := json.Unmarshal(request.Input, &instructions); err != nil {
			return nil, err
		}
		if instructions.Revision == "" {
			return nil, errors.New("配置版本冲突")
		}
		if len(instructions.Content) > 1024*1024 {
			return nil, errors.New("配置长度超限")
		}
	}
	if method == "config.provider.write" && input.Revision == "" {
		return nil, errors.New("配置版本冲突")
	}
	if request.Engine == runtimeidentity.Claude {
		service := options.Claude
		if service == nil {
			return nil, errors.New("尚未启用 Claude 配置服务")
		}
		switch method {
		case "config.read":
			return service.Read()
		case "config.agents.write":
			return service.UpdateAgents(instructions.Revision, instructions.Content)
		case "config.provider.write":
			return service.UpdateProvider(input)
		case "runtime.restart":
			return nil, service.Restart()
		}
	} else {
		service := options.Service
		if service == nil {
			return nil, errors.New("尚未启用 Codex 配置服务")
		}
		switch method {
		case "config.read":
			return service.Read()
		case "config.agents.write":
			return service.UpdateAgents(instructions.Revision, instructions.Content)
		case "config.provider.write":
			return service.UpdateProvider(input.Revision, input.BaseURL, input.APIKey, input.ClearAPIKey)
		case "runtime.restart":
			return nil, service.Restart()
		}
	}
	return nil, fmt.Errorf("不支持的运行时配置方法 %q", method)
}
