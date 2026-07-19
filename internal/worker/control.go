package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	toolservice "github.com/slovx2/tyrs-hand/internal/tools"
)

type ControlClient struct {
	baseURL string
	client  *http.Client
}

func NewControlClient(baseURL string, timeout time.Duration) *ControlClient {
	return &ControlClient{baseURL: baseURL, client: &http.Client{Timeout: timeout}}
}

func (c *ControlClient) CallTool(ctx context.Context, capability string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	payload := toolservice.CallRequest{
		Capability: capability, ThreadID: request.ThreadID, TurnID: request.TurnID,
		CallID: request.CallID, Namespace: namespace, Tool: request.Tool, Arguments: request.Arguments,
	}
	var result codex.ToolCallResult
	if err := c.post(ctx, "/internal/v1/tools/call", payload, &result); err != nil {
		return codex.ToolCallResult{}, err
	}
	return result, nil
}

func (c *ControlClient) GitCredential(ctx context.Context, capability, purpose string, turn ...string) (string, error) {
	threadID, turnID := "", ""
	if len(turn) >= 2 {
		threadID, turnID = turn[0], turn[1]
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := c.post(ctx, "/internal/v1/git/credential", map[string]string{
		"capability": capability, "purpose": purpose, "threadId": threadID, "turnId": turnID,
	}, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("控制面没有返回 Git 凭据")
	}
	return response.Token, nil
}

func (c *ControlClient) ReportFailure(ctx context.Context, capability, code string) error {
	return c.post(ctx, "/internal/v1/tools/failure", map[string]string{
		"capability": capability, "code": code,
	}, nil)
}

func (c *ControlClient) post(ctx context.Context, path string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &problem)
		return fmt.Errorf("控制面返回 %s: %s", response.Status, problem.Detail)
	}
	if output != nil && len(body) > 0 {
		return json.Unmarshal(body, output)
	}
	return nil
}
