package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (p *RemoteProcessor) handleRemoteInteractive(ctx context.Context, task *workerprotocol.Task,
	generation int64, request codex.ServerRequest,
) (any, error) {
	state, err := p.client.RegisterInteractive(ctx, task, request.ID, request.Params, generation)
	if err != nil {
		return nil, err
	}
	for {
		switch state.Status {
		case "resolved", "expired":
			if state.Ready {
				if len(state.Answer) == 0 {
					return json.RawMessage(`{"answers":{}}`), nil
				}
				return state.Answer, nil
			}
		case "interrupted":
			return nil, errors.New("app-server 重启中断了 requestUserInput")
		}
		if !waitContext(ctx, 250*time.Millisecond) {
			return nil, ctx.Err()
		}
		state, err = p.client.InteractiveState(ctx, state.ID)
		if err != nil {
			return nil, err
		}
	}
}
