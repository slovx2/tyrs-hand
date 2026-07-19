package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_FAKE_CODEX") == "1" {
		runFakeCodex()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClientLifecycleAndDynamicTool(t *testing.T) {
	toolCalled := make(chan ToolCallRequest, 1)
	client, err := Start(context.Background(), ClientOptions{
		Bin: os.Args[0], CWD: t.TempDir(), CodexHome: t.TempDir(),
		Environment: []string{"GO_WANT_FAKE_CODEX=1"}, RequestTimeout: 2 * time.Second,
		ToolHandler: func(_ context.Context, request ToolCallRequest) (ToolCallResult, error) {
			toolCalled <- request
			return TextToolResult("ok", true), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runtime := NewRuntime(client)

	threadID, err := runtime.StartThread(context.Background(), ports.ThreadOptions{
		CWD: t.TempDir(), Sandbox: "workspace-write", ApprovalPolicy: "never", NetworkEnabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "thread-1", threadID)
	turnID, err := runtime.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "hello", ClientUserMessageID: "message-1"})
	require.NoError(t, err)
	require.Equal(t, "turn-1", turnID)

	select {
	case request := <-toolCalled:
		require.Equal(t, "github", *request.Namespace)
		require.Equal(t, "issue_read", request.Tool)
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到 dynamic tool call")
	}
}

func TestClientMatchesOutOfOrderAndIgnoresDuplicateResponses(t *testing.T) {
	client := startFakeClient(t, "out-of-order", 2*time.Second)
	var group sync.WaitGroup
	errorsByMethod := make(chan error, 2)
	call := func(method string) {
		group.Add(1)
		go func(method string) {
			defer group.Done()
			var result struct {
				Method string `json:"method"`
			}
			err := client.Call(context.Background(), method, map[string]any{}, &result)
			if err == nil && result.Method != method {
				err = fmt.Errorf("响应错配: %s != %s", result.Method, method)
			}
			errorsByMethod <- err
		}(method)
	}
	call("test/slow")
	time.Sleep(20 * time.Millisecond)
	call("test/fast")
	group.Wait()
	close(errorsByMethod)
	for err := range errorsByMethod {
		require.NoError(t, err)
	}
	var result map[string]any
	require.NoError(t, client.Call(context.Background(), "test/duplicate", map[string]any{}, &result))
	require.NoError(t, client.Call(context.Background(), "test/after-duplicate", map[string]any{}, &result))
}

func TestClientTimeoutAndProcessExitRejectPendingRequest(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		client := startFakeClient(t, "timeout", 2*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := client.Call(ctx, "test/timeout", map[string]any{}, nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("exit", func(t *testing.T) {
		client := startFakeClient(t, "exit", 2*time.Second)
		err := client.Call(context.Background(), "test/exit", map[string]any{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Codex App Server")
	})
}

func TestReadLoopOwnsEventChannelLifecycle(t *testing.T) {
	reader, writer := io.Pipe()
	client := &Client{
		options: ClientOptions{Logger: zap.NewNop()},
		pending: make(map[int64]chan rpcMessage),
		events:  make(chan Event, 1),
		done:    make(chan struct{}),
	}
	readDone := make(chan struct{})
	go func() {
		client.readLoop(reader)
		close(readDone)
	}()

	client.fail(io.EOF)
	_, err := io.WriteString(writer, `{"method":"turn/completed","params":{}}`+"\n")
	require.NoError(t, err)
	select {
	case event := <-client.Events():
		require.Equal(t, "turn/completed", event.Method)
	case <-time.After(time.Second):
		t.Fatal("进程退出后的尾部事件未被读取")
	}
	require.NoError(t, writer.Close())
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Codex 事件读取循环未退出")
	}
	_, open := <-client.Events()
	require.False(t, open)
}

func TestRuntimeResumeSteerAndInterrupt(t *testing.T) {
	client := startFakeClient(t, "", 2*time.Second)
	runtime := NewRuntime(client)
	options := ports.ThreadOptions{CWD: t.TempDir(), Sandbox: "workspace-write", ApprovalPolicy: "never"}
	require.NoError(t, runtime.ResumeThread(context.Background(), "thread-1", options))
	require.NoError(t, runtime.SteerTurn(context.Background(), "thread-1", "turn-1", ports.TurnInput{
		Text:                "more",
		ClientUserMessageID: "message-2",
	}))
	require.NoError(t, runtime.InterruptTurn(context.Background(), "thread-1", "turn-1"))
}

func startFakeClient(t *testing.T, mode string, timeout time.Duration) *Client {
	t.Helper()
	client, err := Start(context.Background(), ClientOptions{
		Bin: os.Args[0], CWD: t.TempDir(), CodexHome: t.TempDir(),
		Environment: []string{"GO_WANT_FAKE_CODEX=1", "FAKE_CODEX_MODE=" + mode}, RequestTimeout: timeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func runFakeCodex() {
	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	encoder := json.NewEncoder(os.Stdout)
	mode := os.Getenv("FAKE_CODEX_MODE")
	var slowID any
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		method, _ := message["method"].(string)
		id := message["id"]
		switch method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"serverInfo": map[string]any{"name": "fake"}}})
		case "initialized":
			initialized = true
		case "thread/start":
			if !initialized {
				os.Exit(3)
			}
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "turn/start":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			_ = encoder.Encode(map[string]any{
				"id": 99, "method": "item/tool/call",
				"params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "callId": "call-1", "namespace": "github", "tool": "issue_read", "arguments": map[string]any{"owner": "o"}},
			})
		case "turn/steer", "turn/interrupt", "thread/resume":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{}})
		case "skills/list":
			if mode == "skills" {
				_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"data": []map[string]any{{
					"cwd":    message["params"].(map[string]any)["cwds"].([]any)[0],
					"skills": []map[string]any{{"name": "demo", "path": os.Getenv("FAKE_SKILL_PATH"), "enabled": true}},
					"errors": []any{},
				}}}})
			} else {
				_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{}})
			}
		case "test/slow":
			slowID = id
		case "test/fast":
			if mode == "out-of-order" {
				_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"method": method}})
				_ = encoder.Encode(map[string]any{"id": slowID, "result": map[string]any{"method": "test/slow"}})
			}
		case "test/duplicate":
			result := map[string]any{"id": id, "result": map[string]any{"method": method}}
			_ = encoder.Encode(result)
			_ = encoder.Encode(result)
		case "test/after-duplicate":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"method": method}})
		case "test/timeout":
			// 保持请求悬挂，由客户端上下文负责超时。
		case "test/exit":
			os.Exit(4)
		default:
			if id != nil {
				_ = encoder.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": fmt.Sprintf("unknown %s", method)}})
			}
		}
	}
}
