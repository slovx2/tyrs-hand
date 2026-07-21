package workerprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type Client struct {
	baseURL    string
	credential string
	http       *http.Client
}

type HTTPError struct {
	StatusCode int
	Status     string
	Detail     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("control 返回 %s: %s", e.Status, e.Detail)
}

func IsLeaseLost(err error) bool {
	var response *HTTPError
	return errors.As(err, &response) && response.StatusCode == http.StatusConflict
}

func IsAlreadyFinished(err error) bool {
	var response *HTTPError
	return errors.As(err, &response) && response.StatusCode == http.StatusGone
}

func NewClient(baseURL, credential string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), credential: credential,
		http: &http.Client{Timeout: timeout}}
}

func (c *Client) SetCredential(value string) { c.credential = value }

func (c *Client) Enroll(ctx context.Context, token string) (EnrollResponse, error) {
	var result EnrollResponse
	err := c.call(ctx, http.MethodPost, "/worker/v1/enroll", EnrollRequest{Token: token}, &result, false)
	return result, err
}

func (c *Client) Heartbeat(ctx context.Context, request HeartbeatRequest) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/heartbeat", request, nil, true)
}

func (c *Client) Claim(ctx context.Context, request ClaimRequest) (ClaimResponse, error) {
	var response ClaimResponse
	if err := c.call(ctx, http.MethodPost, "/worker/v1/claims", request, &response, true); err != nil {
		return ClaimResponse{}, err
	}
	return response, nil
}

func (c *Client) DevelopmentOperationHeartbeat(ctx context.Context,
	operation *DevelopmentOperation,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/development-operations/"+
		operation.ID.String()+"/heartbeat", DevelopmentOperationLease{
		LeaseToken: operation.LeaseToken, LeaseEpoch: operation.LeaseEpoch,
	}, nil, true)
}

func (c *Client) CompleteDevelopmentOperation(ctx context.Context,
	operation *DevelopmentOperation,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/development-operations/"+
		operation.ID.String()+"/complete", DevelopmentOperationTerminal{
		DevelopmentOperationLease: DevelopmentOperationLease{
			LeaseToken: operation.LeaseToken, LeaseEpoch: operation.LeaseEpoch,
		},
		IdempotencyKey: operation.ID.String() + ":complete",
	}, nil, true)
}

func (c *Client) FailDevelopmentOperation(ctx context.Context,
	operation *DevelopmentOperation, cause error,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return c.call(ctx, http.MethodPost, "/worker/v1/development-operations/"+
		operation.ID.String()+"/fail", DevelopmentOperationTerminal{
		DevelopmentOperationLease: DevelopmentOperationLease{
			LeaseToken: operation.LeaseToken, LeaseEpoch: operation.LeaseEpoch,
		},
		IdempotencyKey: operation.ID.String() + ":fail", Error: message,
	}, nil, true)
}

func lease(task *Task) RunLeaseRequest {
	return RunLeaseRequest{LeaseToken: task.Claimed.LeaseToken,
		LeaseEpoch: task.Claimed.LeaseEpoch}
}

func runPath(task *Task, suffix string) string {
	return "/worker/v1/runs/" + task.Claimed.RunID.String() + suffix
}

func (c *Client) RunHeartbeat(ctx context.Context, task *Task) (RunHeartbeatResponse, error) {
	var response RunHeartbeatResponse
	err := c.call(ctx, http.MethodPost, runPath(task, "/heartbeat"), lease(task),
		&response, true)
	return response, err
}

func (c *Client) AckCommand(ctx context.Context, task *Task, command RunCommand,
	action, turnID string,
) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/commands/ack"), CommandAckRequest{
		RunLeaseRequest: lease(task), CommandID: command.ID, Action: action, TurnID: turnID,
	}, nil, true)
}

func (c *Client) Events(ctx context.Context, task *Task, events []EventInput) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/events"), EventsRequest{
		RunLeaseRequest: lease(task), Events: events,
	}, nil, true)
}

func (c *Client) Complete(ctx context.Context, task *Task, result codexcontrol.TurnResult) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/complete"), CompleteRequest{
		RunLeaseRequest: lease(task), IdempotencyKey: task.Claimed.RunID.String() + ":complete",
		Result: result,
	}, nil, true)
}

func (c *Client) CompleteDomain(ctx context.Context, task *Task, result CompleteRequest) error {
	result.RunLeaseRequest = lease(task)
	if result.IdempotencyKey == "" {
		result.IdempotencyKey = task.Claimed.RunID.String() + ":complete"
	}
	return c.call(ctx, http.MethodPost, runPath(task, "/complete"), result, nil, true)
}

func (c *Client) Fail(ctx context.Context, task *Task, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return c.call(ctx, http.MethodPost, runPath(task, "/fail"), FailRequest{
		RunLeaseRequest: lease(task), IdempotencyKey: task.Claimed.RunID.String() + ":fail",
		Code: code, Message: message,
	}, nil, true)
}

func (c *Client) RuntimeCredential(ctx context.Context, task *Task) (RuntimeCredential, error) {
	var result RuntimeCredential
	err := c.call(ctx, http.MethodPost, runPath(task, "/runtime-credential"), lease(task),
		&result, true)
	return result, err
}

func (c *Client) DownloadAttachment(ctx context.Context, task *Task, attachmentID uuid.UUID,
	destination io.Writer,
) (string, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+runPath(task, "/attachments/")+attachmentID.String(), nil)
	if err != nil {
		return "", 0, err
	}
	if c.credential == "" {
		return "", 0, errors.New("执行节点尚未注册")
	}
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("X-Run-Lease-Token", task.Claimed.LeaseToken)
	request.Header.Set("X-Run-Lease-Epoch", fmt.Sprintf("%d", task.Claimed.LeaseEpoch))
	response, err := c.http.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return "", 0, &HTTPError{StatusCode: response.StatusCode, Status: response.Status,
			Detail: strings.TrimSpace(string(data))}
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, (25<<20)+1))
	if err == nil && written > 25<<20 {
		err = errors.New("control 返回的 Discord 附件超过大小限制")
	}
	return response.Header.Get("X-Attachment-SHA256"), written, err
}

func (c *Client) SetThread(ctx context.Context, task *Task, threadID, home, signature string) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/thread"), SetThreadRequest{
		RunLeaseRequest: lease(task), ThreadID: threadID, CodexHome: home,
		ProviderSignature: signature,
	}, nil, true)
}

func (c *Client) RecordSubmission(ctx context.Context, task *Task, id string) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/submission"), SubmissionRequest{
		RunLeaseRequest: lease(task), SubmissionID: id,
	}, nil, true)
}

func (c *Client) ConfirmTurn(ctx context.Context, task *Task, id string) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/confirm"), ConfirmTurnRequest{
		RunLeaseRequest: lease(task), TurnID: id,
	}, nil, true)
}

func (c *Client) DevelopmentState(ctx context.Context, task *Task, state DevelopmentState) error {
	state.RunLeaseRequest = lease(task)
	return c.call(ctx, http.MethodPost, runPath(task, "/development-state"), state, nil, true)
}

func (c *Client) WorkspaceState(ctx context.Context, task *Task, state WorkspaceState) error {
	state.RunLeaseRequest = lease(task)
	return c.call(ctx, http.MethodPost, runPath(task, "/workspace-state"), state, nil, true)
}

func (c *Client) SetDiscordTitle(ctx context.Context, task *Task,
	title string,
) (DiscordTitleResponse, error) {
	var response DiscordTitleResponse
	err := c.call(ctx, http.MethodPost, runPath(task, "/discord-title"), DiscordTitleRequest{
		RunLeaseRequest: lease(task), Title: title,
	}, &response, true)
	return response, err
}

func (c *Client) CallTool(ctx context.Context, task *Task,
	request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	var result codex.ToolCallResult
	err := c.call(ctx, http.MethodPost, runPath(task, "/tools/call"), ToolCallRequest{
		RunLeaseRequest: lease(task), Capability: task.Claimed.Capability, Request: request,
	}, &result, true)
	return result, err
}

func (c *Client) GitCredential(ctx context.Context, task *Task, purpose, threadID,
	turnID string,
) (string, error) {
	var response struct {
		Token string `json:"token"`
	}
	err := c.call(ctx, http.MethodPost, runPath(task, "/git-credential"), GitCredentialRequest{
		RunLeaseRequest: lease(task), Capability: task.Claimed.Capability, Purpose: purpose,
		ThreadID: threadID, TurnID: turnID,
	}, &response, true)
	return response.Token, err
}

func (c *Client) call(ctx context.Context, method, path string, input, output any,
	authenticated bool,
) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if authenticated {
		if c.credential == "" {
			return errors.New("执行节点尚未注册")
		}
		request.Header.Set("Authorization", "Bearer "+c.credential)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{StatusCode: response.StatusCode, Status: response.Status,
			Detail: strings.TrimSpace(string(data))}
	}
	if output != nil && len(data) > 0 {
		return json.Unmarshal(data, output)
	}
	return nil
}
