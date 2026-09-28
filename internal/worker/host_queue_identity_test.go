package worker

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestHostQueueBindingCannotAdoptNewWorkspace(t *testing.T) {
	for _, bound := range []bool{false, true} {
		name := "未绑定队列"
		if bound {
			name = "旧绑定队列"
		}
		t.Run(name, func(t *testing.T) {
			c := NewHostDesktopController(&Processor{}, nil)
			manifest := &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.New(), OwnerParticipant: &workerprotocol.ParticipantIdentity{ParticipantID: uuid.New(), DisplayName: "原负责人"}}
			if bound {
				c.setBinding(manifest)
			}
			original, _ := c.snapshot()
			ready := make(chan struct{})
			close(ready)
			execution := &hostQueueExecution{ready: ready, controller: original}
			c.queued["thread"] = &hostQueueState{items: map[string]*hostQueueItem{"client": {controller: original}}, turns: map[string]*hostQueueExecution{"turn": execution}}
			next := &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.New(), OwnerParticipant: &workerprotocol.ParticipantIdentity{ParticipantID: uuid.New(), DisplayName: "新负责人"}}
			c.setBinding(next)
			current, _ := c.snapshot()
			require.False(t, current.workspace.metadataAllowed("thread"), "新绑定不能接收旧队列 metadata")
			plan, err := c.PrepareCall(t.Context(), appserverhub.Call{Method: "turn/steer", Role: appserverhub.RoleDesktop,
				Params: json.RawMessage(`{"threadId":"thread","expectedTurnId":"turn","input":[]}`)})
			require.NoError(t, err)
			require.Same(t, original, plan.State.(*hostCallState).controller)
			require.NotContains(t, string(plan.Params), "新负责人")
			entry := &hostQueueJournal{}
			if bound {
				entry.Manifest = manifest
			}
			restored := c.queueController(entry, &hostworker.Runtime{})
			if bound {
				require.NotNil(t, restored)
				require.False(t, restored.controlEnabled(), "重启也不能恢复旧绑定的 Control 授权")
				require.Equal(t, manifest.WorkspaceID, restored.workspace.manifest.WorkspaceID)
			} else {
				require.Nil(t, restored)
			}
			delete(c.queued, "thread")
			require.True(t, current.workspace.metadataAllowed("thread"))
		})
	}
}
