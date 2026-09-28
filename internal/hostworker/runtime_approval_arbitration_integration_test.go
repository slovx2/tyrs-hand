//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeApprovalArbitrationRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "approval-arbitration")
}

// APPROVAL-001：真实多端待办资格校验和首个有效答案仲裁，验证原生工具文件副作用。
func verifyRuntimeApprovalArbitration(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeParallelApprovalFixture) {
	t.Helper()
	clients := make([]*codex.SocketClient, 2)
	traces := make([]*protocolTraceTransport, 2)
	prompts := []chan runtimeApprovalPrompt{make(chan runtimeApprovalPrompt, 4), make(chan runtimeApprovalPrompt, 4)}
	for index := range clients {
		clients[index], traces[index] = connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{
			ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
				prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1), cancelled: make(chan struct{})}
				select {
				case prompts[index] <- prompt:
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
				select {
				case answer := <-prompt.reply:
					return answer, nil
				case <-callbackCtx.Done():
					close(prompt.cancelled)
					return nil, callbackCtx.Err()
				}
			},
		})
	}
	for round, decision := range []string{"accept", "decline"} {
		thread := readSessionThread(t, ctx, clients[0], "thread/start", map[string]any{
			"cwd": filepath.Join(fixture.root, "project"), "approvalPolicy": "on-request", "sandbox": "danger-full-access"})
		readSessionThread(t, ctx, clients[1], "thread/resume", map[string]any{"threadId": thread.ID})
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, clients[0].Call(ctx, "turn/start", map[string]any{
			"threadId": thread.ID, "input": []map[string]string{{"type": "text", "text": "PARALLEL_CLAUDE_" + decision}}}, &started))
		pending := make([]runtimeApprovalPrompt, 2)
		for index := range clients {
			select {
			case pending[index] = <-prompts[index]:
			case <-ctx.Done():
				t.Fatal("两个真实客户端必须收到同一审批")
			}
			require.Equal(t, "item/fileChange/requestApproval", pending[index].request.Method)
			var identity struct{ ThreadID, TurnID string }
			require.NoError(t, json.Unmarshal(pending[index].request.Params, &identity))
			require.Equal(t, thread.ID, identity.ThreadID)
			require.Equal(t, started.Turn.ID, identity.TurnID)
		}
		require.JSONEq(t, string(pending[0].request.ID), string(pending[1].request.ID))
		path := filepath.Join(fixture.root, "project", "parallel-claude-"+decision+".txt")
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err), "审批前不得产生文件副作用")
		if round == 0 {
			// 已收到请求也不代表永久拥有回答资格：取消订阅后先到的拒绝必须无效。
			require.NoError(t, clients[1].Call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.ID}, nil))
			pending[1].reply <- map[string]string{"decision": "decline"}
			for until := time.Now().Add(250 * time.Millisecond); time.Now().Before(until); {
				_, statErr := os.Stat(path)
				require.True(t, os.IsNotExist(statErr), "无资格答案不得执行工具")
				require.Equal(t, int64(1), fixture.claudeCalls.Load(), "无资格答案不得恢复原生模型")
				time.Sleep(10 * time.Millisecond)
			}
			pending[0].reply <- map[string]string{"decision": "accept"}
		} else {
			pending[1].reply <- map[string]string{"decision": "decline"}
			select {
			case <-pending[0].cancelled:
			case <-ctx.Done():
				t.Fatal("首个有效答案必须取消另一客户端待办")
			}
			// 真实投递迟到答案；不能通过修改 SDK 或构造上游通知来模拟仲裁成功。
			late, marshalErr := json.Marshal(map[string]any{"id": pending[0].request.ID, "result": map[string]string{"decision": "accept"}})
			require.NoError(t, marshalErr)
			require.NoError(t, traces[0].WriteMessage(1, late))
		}
		waitSessionTurn(t, ctx, clients[0], thread.ID, started.Turn.ID)
		require.Equal(t, int64((round+1)*2), fixture.claudeCalls.Load(), "只执行一次工具和一次后续模型请求")
		if decision == "accept" {
			contents, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, "accept", string(contents))
		} else {
			_, statErr := os.Stat(path)
			require.True(t, os.IsNotExist(statErr), "迟到允许不得推翻已拒绝的审批")
		}
		for _, client := range clients {
			require.NoError(t, client.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.ID}, nil))
		}
	}
	for _, queued := range prompts {
		require.Empty(t, queued, "重复答案不能产生新审批")
	}
}
