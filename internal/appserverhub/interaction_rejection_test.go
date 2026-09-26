package appserverhub_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

type rejectedInteractiveController struct {
	appserverhub.PassThroughController
}

func (rejectedInteractiveController) ResolveInteractive(context.Context, codex.ServerRequest,
	json.RawMessage, appserverhub.Role,
) (bool, json.RawMessage, error) {
	return false, nil, errors.New("Control 拒绝当前会话的审批答案")
}

// 仅验证 Hub 的错误传播；真实 SDK、SSH 与工具副作用由运行时专项验收。
func TestHubRejectedInteractiveAnswerFailsWithoutWaitingForTimeout(t *testing.T) {
	for _, method := range []string{"item/fileChange/requestApproval", "item/tool/requestUserInput"} {
		t.Run(method, func(t *testing.T) {
			mock, err := mockcodex.Start(t)
			require.NoError(t, err)
			hub, err := appserverhub.Start(t.Context(), appserverhub.Options{
				SocketPath: shortTempDir(t) + "/hub.sock", UpstreamSocketPath: mock.SocketPath,
				Controller: rejectedInteractiveController{}, ServerRequestTimeout: 5 * time.Second,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, hub.Close()) })
			desktop := connectDesktop(t, hub.SocketPath())
			desktop.initialize(t, 1)
			desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
				Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
			threadID := responseThreadID(t, desktop.response(t, rawID(2)).Result)
			requestID := mock.RequestServer(threadID, method, map[string]any{"turnId": "turn", "itemId": "item"})
			request := desktop.serverRequest(t, method)
			answer := map[string]any{"decision": "accept"}
			if method == "item/tool/requestUserInput" {
				answer = map[string]any{"answers": map[string]any{"q": map[string]any{"answers": []string{"yes"}}}}
			}
			desktop.write(t, rpcMessage{ID: request.ID, Result: mustJSON(answer)})
			require.Eventually(t, func() bool {
				result, responses, resolved := mock.ResolvedRequest(requestID)
				return resolved && responses == 1 && len(result) == 0
			}, time.Second, 5*time.Millisecond, "明确仲裁错误必须立即返回，不能吞掉后等待审批超时")
		})
	}
}
