package workerprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
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

var errNotModified = errors.New("Worker 配置未变化")

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
	err := c.call(ctx, http.MethodPost, "/worker/v1/enroll", EnrollRequest{Token: token},
		&result, false)
	return result, err
}

func (c *Client) Heartbeat(ctx context.Context, request HeartbeatRequest) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/heartbeat", request, nil, true)
}

func (c *Client) SSHConfiguration(ctx context.Context, etag string) (SSHConfiguration, string, bool, error) {
	var configuration SSHConfiguration
	if err := c.callWithParameters(ctx, http.MethodGet, "/worker/v1/ssh-configuration",
		map[string]string{"ifNoneMatch": etag}, nil, &configuration, true); err != nil {
		if errors.Is(err, errNotModified) {
			return SSHConfiguration{}, etag, false, nil
		}
		return SSHConfiguration{}, etag, false, err
	}
	nextETag := `"` + configuration.Revision + `"`
	return configuration, nextETag, nextETag != etag, nil
}

func (c *Client) Workspace(ctx context.Context) (*WorkspaceManifest, error) {
	var result struct {
		Workspace *WorkspaceManifest `json:"workspace"`
	}
	err := c.call(ctx, http.MethodGet, "/worker/v1/workspace", nil, &result, true)
	return result.Workspace, err
}

func (c *Client) WorkspaceProjectSnapshot(ctx context.Context,
	request WorkspaceProjectSnapshotRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/workspace/projects/snapshot",
		request, nil, true)
}

func (c *Client) PrepareDesktopThread(ctx context.Context,
	request DesktopThreadPrepareRequest,
) (DesktopThreadState, error) {
	var result DesktopThreadState
	err := c.call(ctx, http.MethodPost, "/worker/v1/desktop-thread-requests", request,
		&result, true)
	return result, err
}

func (c *Client) DesktopThreadState(ctx context.Context,
	requestID uuid.UUID,
) (DesktopThreadState, error) {
	var result DesktopThreadState
	err := c.call(ctx, http.MethodGet, "/worker/v1/desktop-thread-requests/"+
		requestID.String(), nil, &result, true)
	return result, err
}

func (c *Client) CompleteDesktopThread(ctx context.Context, requestID uuid.UUID,
	request DesktopThreadCompleteRequest,
) (DesktopThreadState, error) {
	var result DesktopThreadState
	err := c.call(ctx, http.MethodPost, "/worker/v1/desktop-thread-requests/"+
		requestID.String()+"/complete", request, &result, true)
	return result, err
}

func (c *Client) FailDesktopThread(ctx context.Context, requestID uuid.UUID,
	request DesktopThreadFailRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/desktop-thread-requests/"+
		requestID.String()+"/fail", request, nil, true)
}

func (c *Client) RecordThreadMetadata(ctx context.Context,
	request ThreadMetadataRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/thread-metadata-events", request, nil, true)
}

func (c *Client) PendingThreadNames(ctx context.Context) ([]ThreadNameUpdate, error) {
	var result []ThreadNameUpdate
	err := c.call(ctx, http.MethodGet, "/worker/v1/thread-name-updates", nil, &result, true)
	return result, err
}

func (c *Client) AckThreadName(ctx context.Context, controlID uuid.UUID,
	request ThreadNameAckRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/thread-name-updates/"+
		controlID.String()+"/ack", request, nil, true)
}

func (c *Client) PrepareDesktopThreadLifecycle(ctx context.Context,
	request ThreadLifecyclePrepareRequest,
) (ThreadLifecycleState, error) {
	var result ThreadLifecycleState
	err := c.call(ctx, http.MethodPost, "/worker/v1/thread-lifecycle-requests/desktop",
		request, &result, true)
	return result, err
}

func (c *Client) PendingThreadLifecycles(ctx context.Context) ([]ThreadLifecycleState, error) {
	var result []ThreadLifecycleState
	err := c.call(ctx, http.MethodGet, "/worker/v1/thread-lifecycle-requests",
		nil, &result, true)
	return result, err
}

func (c *Client) ThreadLifecycleState(ctx context.Context,
	requestID uuid.UUID,
) (ThreadLifecycleState, error) {
	var result ThreadLifecycleState
	err := c.call(ctx, http.MethodGet, "/worker/v1/thread-lifecycle-requests/"+
		requestID.String(), nil, &result, true)
	return result, err
}

func (c *Client) CompleteThreadLifecycle(ctx context.Context, requestID uuid.UUID,
	request ThreadLifecycleCompleteRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/thread-lifecycle-requests/"+
		requestID.String()+"/complete", request, nil, true)
}

func (c *Client) PrepareDesktopTurn(ctx context.Context,
	request DesktopTurnPrepareRequest,
) (Task, error) {
	var result Task
	err := c.call(ctx, http.MethodPost, "/worker/v1/desktop-turns", request, &result, true)
	return result, err
}

func (c *Client) DesktopImageTarget(ctx context.Context,
	intentID uuid.UUID,
) (DesktopImageTarget, error) {
	var result DesktopImageTarget
	err := c.call(ctx, http.MethodGet, "/worker/v1/desktop-turns/"+intentID.String()+
		"/images/target", nil, &result, true)
	return result, err
}

func (c *Client) UploadDesktopImage(ctx context.Context, intentID uuid.UUID,
	ordinal int, image DesktopImage, finalAttempt bool,
) (DesktopImageUploadResult, error) {
	var result DesktopImageUploadResult
	file, err := os.Open(image.SourcePath)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	return c.UploadDesktopImageReader(ctx, intentID, ordinal, image, finalAttempt, file)
}

func (c *Client) UploadDesktopImageReader(ctx context.Context, intentID uuid.UUID,
	ordinal int, image DesktopImage, finalAttempt bool, source io.Reader,
) (DesktopImageUploadResult, error) {
	var result DesktopImageUploadResult
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeResult := make(chan error, 1)
	go func() {
		metadata, marshalErr := json.Marshal(DesktopImageUploadMetadata{FinalAttempt: finalAttempt})
		if marshalErr == nil {
			var field io.Writer
			field, marshalErr = multipartWriter.CreateFormField("metadata")
			if marshalErr == nil {
				_, marshalErr = field.Write(metadata)
			}
		}
		if marshalErr == nil {
			var part io.Writer
			part, marshalErr = multipartWriter.CreateFormFile("file", image.Filename)
			if marshalErr == nil {
				_, marshalErr = io.Copy(part, source)
			}
		}
		if closeErr := multipartWriter.Close(); marshalErr == nil {
			marshalErr = closeErr
		}
		_ = writer.CloseWithError(marshalErr)
		writeResult <- marshalErr
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/worker/v1/blobs/%s?ordinal=%d", c.baseURL, intentID, ordinal),
		reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return result, err
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	err = c.execute(request, &result, true)
	_ = reader.CloseWithError(err)
	if writeErr := <-writeResult; err == nil {
		err = writeErr
	}
	return result, err
}

func (c *Client) FailDesktopImage(ctx context.Context, intentID uuid.UUID,
	ordinal int, cause error,
) error {
	message := "图片同步失败"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	return c.call(ctx, http.MethodPost, fmt.Sprintf(
		"/worker/v1/desktop-turns/%s/images/%d/fail", intentID, ordinal),
		DesktopImageFailureRequest{Error: message}, nil, true)
}

func (c *Client) PrepareDesktopRollback(ctx context.Context,
	request DesktopRollbackPrepareRequest,
) (DesktopRollbackState, error) {
	var result DesktopRollbackState
	err := c.call(ctx, http.MethodPost, "/worker/v1/desktop-rollbacks", request, &result, true)
	return result, err
}

func (c *Client) CompleteDesktopRollback(ctx context.Context, requestID uuid.UUID,
	request DesktopRollbackCompleteRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/desktop-rollbacks/"+requestID.String()+
		"/complete", request, nil, true)
}

func (c *Client) PreflightDesktopTurn(ctx context.Context,
	request DesktopTurnPreflightRequest,
) (DesktopTurnPreflightResponse, error) {
	var result DesktopTurnPreflightResponse
	err := c.call(ctx, http.MethodPost, "/worker/v1/desktop-turns/preflight", request, &result, true)
	return result, err
}

func (c *Client) RecordDesktopSteer(ctx context.Context,
	request DesktopSteerRecordRequest,
) error {
	return c.call(ctx, http.MethodPost, "/worker/v1/desktop-steers", request, nil, true)
}

func (c *Client) RegisterInteractive(ctx context.Context, task *Task,
	requestID, params json.RawMessage, generation int64,
) (InteractiveState, error) {
	var result InteractiveState
	err := c.call(ctx, http.MethodPost, runPath(task, "/interactive"),
		InteractiveRegisterRequest{RunLeaseRequest: lease(task), RequestID: requestID,
			Params: params, AppServerGeneration: generation}, &result, true)
	return result, err
}

func (c *Client) InteractiveState(ctx context.Context,
	requestID uuid.UUID,
) (InteractiveState, error) {
	var result InteractiveState
	err := c.call(ctx, http.MethodGet, "/worker/v1/interactive/"+requestID.String(), nil,
		&result, true)
	return result, err
}

func (c *Client) AnswerInteractive(ctx context.Context,
	request InteractiveAnswerRequest,
) (InteractiveState, error) {
	var result InteractiveState
	err := c.call(ctx, http.MethodPost, "/worker/v1/interactive/answer", request, &result, true)
	return result, err
}

func (c *Client) Claim(ctx context.Context, request ClaimRequest) (ClaimResponse, error) {
	var response ClaimResponse
	if err := c.call(ctx, http.MethodPost, "/worker/v1/claims", request, &response, true); err != nil {
		return ClaimResponse{}, err
	}
	return response, nil
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
	return c.FailWithCodexError(ctx, task, code, cause, nil)
}

func (c *Client) FailWithCodexError(ctx context.Context, task *Task, code string,
	cause error, codexError *CodexTurnError,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return c.call(ctx, http.MethodPost, runPath(task, "/fail"), FailRequest{
		RunLeaseRequest: lease(task), IdempotencyKey: task.Claimed.RunID.String() + ":fail",
		Code: code, Message: message, CodexError: codexError,
	}, nil, true)
}

func (c *Client) DownloadAttachment(ctx context.Context, task *Task, attachmentID uuid.UUID,
	destination io.Writer,
) (string, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/worker/v1/blobs/%s?runId=%s", c.baseURL, attachmentID,
			task.Claimed.RunID), nil)
	if err != nil {
		return "", 0, err
	}
	if c.credential == "" {
		return "", 0, errors.New("Worker尚未注册")
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

func (c *Client) SetThread(ctx context.Context, task *Task, threadID string) error {
	return c.call(ctx, http.MethodPost, runPath(task, "/thread"), SetThreadRequest{
		RunLeaseRequest: lease(task), ThreadID: threadID,
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

func (c *Client) WorkspaceProjectState(ctx context.Context, task *Task, state WorkspaceProjectState) error {
	state.RunLeaseRequest = lease(task)
	return c.call(ctx, http.MethodPost, runPath(task, "/workspace-project-state"), state, nil, true)
}

func (c *Client) WorkspaceState(ctx context.Context, task *Task, state WorkspaceState) error {
	state.RunLeaseRequest = lease(task)
	return c.call(ctx, http.MethodPost, runPath(task, "/workspace-state"), state, nil, true)
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
	return c.callDirect(ctx, method, path, input, output, authenticated)
}

func (c *Client) callWithParameters(ctx context.Context, method, path string,
	additional map[string]string, input, output any, authenticated bool,
) error {
	return c.callDirectWithHeaders(ctx, method, path, input, output, authenticated,
		map[string]string{"If-None-Match": strings.TrimSpace(additional["ifNoneMatch"])})
}

func (c *Client) callDirect(ctx context.Context, method, path string, input, output any,
	authenticated bool,
) error {
	return c.callDirectWithHeaders(ctx, method, path, input, output, authenticated, nil)
}

func (c *Client) callDirectWithHeaders(ctx context.Context, method, path string, input,
	output any, authenticated bool, headers map[string]string,
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
	for name, value := range headers {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	return c.execute(request, output, authenticated)
}

func (c *Client) execute(request *http.Request, output any, authenticated bool) error {
	if authenticated {
		if c.credential == "" {
			return errors.New("Worker尚未注册")
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
	if response.StatusCode == http.StatusNotModified {
		return errNotModified
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
