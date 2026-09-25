package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestDesktopCannotAcceptControlRejectedApproval(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 410, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				unwrapWorkerTestRequest(t, r)
				var input workerprotocol.InteractiveAnswerRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				require.JSONEq(t, `"native-approval"`, string(input.RequestID))
				require.EqualValues(t, 7, input.AppServerGeneration)
				calls.Add(1)
				http.Error(w, "审批已失效或被拒绝", status)
			}))
			t.Cleanup(server.Close)
			processor := &Processor{cfg: config.Config{ControlTimeout: time.Second},
				client:     workerprotocol.NewClient(server.URL, "mock-only", time.Second),
				workspaces: &workspaceCodexRegistry{ctx: t.Context()}}
			controller := &desktopController{processor: processor, workspace: &workspaceCodex{generation: 7,
				runtime: workspaceRuntime{WorkspaceID: uuid.New()}}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			accepted, answer, err := controller.ResolveInteractive(ctx, codex.ServerRequest{
				ID: json.RawMessage(`"native-approval"`), Method: "item/commandExecution/requestApproval",
				Params: json.RawMessage(`{"threadId":"t","turnId":"u","itemId":"i"}`),
			}, json.RawMessage(`{"decision":"accept"}`), appserverhub.RoleDesktop)
			require.Error(t, err)
			require.False(t, accepted)
			require.Nil(t, answer)
			want := int64(1)
			if status == http.StatusNotFound {
				want = 8 // 允许登记先后顺序的短暂重试，仍不得接受过期请求。
			}
			require.Equal(t, want, calls.Load())
		})
	}
}
