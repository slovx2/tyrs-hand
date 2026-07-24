package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExecLauncherErrorsAreReported(t *testing.T) {
	_, err := Start(context.Background(), ClientOptions{
		Bin: filepath.Join(t.TempDir(), "missing"), CWD: t.TempDir(), CodexHome: t.TempDir(),
	})
	require.ErrorContains(t, err, "启动 Codex")
	file := filepath.Join(t.TempDir(), "not-executable")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err = Start(context.Background(), ClientOptions{Bin: file, CWD: t.TempDir(), CodexHome: t.TempDir()})
	require.Error(t, err)
}

func TestClientKeepsHomeSeparateFromCodexHome(t *testing.T) {
	launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: "/var/lib/tyrs-hand/codex/thread",
		Home: "/home/dev", Launcher: launcher, SkipLocalHome: true, RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	require.Contains(t, launcher.specs[0].Env, "CODEX_HOME=/var/lib/tyrs-hand/codex/thread")
	require.Contains(t, launcher.specs[0].Env, "HOME=/home/dev")
	require.NoError(t, client.Close())
}

func TestClientEnforcesManagedAppServerConfiguration(t *testing.T) {
	launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(),
		Environment: []string{"TYRS_HAND_MODEL_API_KEY=secret"},
		Launcher:    launcher, RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	require.Equal(t, ManagedAppServerArguments("stdio://"), launcher.specs[0].Args)
	require.Contains(t, launcher.specs[0].Args,
		`shell_environment_policy.exclude=["TYRS_HAND_MODEL_API_KEY"]`)
	require.Contains(t, launcher.specs[0].Args, "allow_login_shell=false")
	require.Contains(t, launcher.specs[0].Args,
		`openai_base_url="https://chatgpt.com/backend-api/codex"`)
	require.NoError(t, client.Close())
}

func TestReadFrameBoundaries(t *testing.T) {
	frame, err := readFrame(bufio.NewReader(strings.NewReader("1234\n")), 5)
	require.NoError(t, err)
	require.Equal(t, "1234", string(frame))
	_, err = readFrame(bufio.NewReader(strings.NewReader("12345\n")), 5)
	require.ErrorContains(t, err, "超过")
	frame, err = readFrame(bufio.NewReader(strings.NewReader(`{"id":1}`)), 32)
	require.ErrorIs(t, err, io.EOF)
	require.NotEmpty(t, frame)
}

func TestClientRejectsInvalidAndPartialFrames(t *testing.T) {
	for _, test := range []struct{ name, output string }{
		{"invalid", "not-json\n"}, {"partial", `{"id":1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			launcher := &scriptedLauncher{script: func(server *scriptedServer) {
				_ = server.request()
				server.raw(test.output)
			}}
			_, err := Start(context.Background(), ClientOptions{
				Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
				RequestTimeout: time.Second,
			})
			require.Error(t, err)
		})
	}
}

func TestClientDoesNotDropBurstLargerThanLegacyQueue(t *testing.T) {
	launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
		for index := 0; index < 300; index++ {
			server.send(map[string]any{"method": "item/delta", "params": map[string]any{"turnId": "turn"}})
		}
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
		EventBacklog: 512, RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	for index := 0; index < 300; index++ {
		select {
		case event := <-client.Events():
			require.Equal(t, "item/delta", event.Method)
		case <-time.After(time.Second):
			t.Fatalf("第 %d 条事件丢失", index)
		}
	}
	require.NoError(t, client.Close())
}

func TestBacklogOverflowFailsSession(t *testing.T) {
	launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
		request := server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{}})
		for index := 0; index < 3; index++ {
			server.send(map[string]any{"method": "event", "params": map[string]any{}})
		}
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
		EventBacklog: 2, RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	if callErr := client.Call(context.Background(), "burst", map[string]any{}, nil); callErr != nil {
		require.ErrorContains(t, callErr, "backlog")
	}
	select {
	case <-client.Done():
		require.ErrorContains(t, client.processError(), "backlog")
	case <-time.After(time.Second):
		t.Fatal("Backlog 溢出没有终止 Session")
	}
}

func TestRequestErrorStates(t *testing.T) {
	launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
		request := server.request()
		server.send(map[string]any{"id": request["id"], "error": map[string]any{"code": -1, "message": "no"}})
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
		RequestTimeout: time.Second,
	})
	require.NoError(t, err)
	err = client.Call(context.Background(), "turn/start", map[string]any{}, nil)
	var requestErr *RequestError
	require.ErrorAs(t, err, &requestErr)
	require.Equal(t, RequestRejected, requestErr.State)
	require.NoError(t, client.Close())

	err = (&RequestError{State: RequestUnknown, Cause: context.DeadlineExceeded})
	require.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestCloseEscalatesToKillWhenSignalIgnored(t *testing.T) {
	launcher := &scriptedLauncher{ignoreSignal: true, script: initializeScript(func(server *scriptedServer) {
		<-server.process.exited
	})}
	client, err := Start(context.Background(), ClientOptions{
		Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
		RequestTimeout: time.Second, CloseTimeout: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, client.Close())
	process := launcher.processes[0]
	process.mu.Lock()
	defer process.mu.Unlock()
	require.Equal(t, 1, process.signals)
	require.Equal(t, 1, process.kills)
}

func TestToolHandlerPanicAndTimeoutReturnFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler ToolHandler
	}{
		{"panic", func(context.Context, ToolCallRequest) (ToolCallResult, error) { panic("boom") }},
		{"timeout", func(ctx context.Context, _ ToolCallRequest) (ToolCallResult, error) {
			<-ctx.Done()
			return ToolCallResult{}, ctx.Err()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			responseSeen := make(chan map[string]any, 1)
			launcher := &scriptedLauncher{script: initializeScript(func(server *scriptedServer) {
				server.send(map[string]any{"id": 77, "method": "item/tool/call", "params": map[string]any{
					"threadId": "thread", "turnId": "turn", "callId": "call",
					"namespace": "github", "tool": "read", "arguments": map[string]any{},
				}})
				responseSeen <- server.request()
				<-server.process.exited
			})}
			client, err := Start(context.Background(), ClientOptions{
				Bin: "mock", CWD: t.TempDir(), CodexHome: t.TempDir(), Launcher: launcher,
				RequestTimeout: time.Second, ToolTimeout: 20 * time.Millisecond, ToolHandler: test.handler,
			})
			require.NoError(t, err)
			select {
			case response := <-responseSeen:
				require.Equal(t, float64(77), response["id"])
				result := response["result"].(map[string]any)
				require.Equal(t, false, result["success"])
			case <-time.After(time.Second):
				t.Fatal("Tool Handler 异常没有写回失败结果")
			}
			require.NoError(t, client.Close())
		})
	}
}
