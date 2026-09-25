package appserverhub

import (
	"context"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

// 只重新发送待回答的交互，绝不重新派发已执行或结果不确定的动态工具。
func (r *Hub) waitInteractiveAnswer(ctx context.Context, request codex.ServerRequest, threadID string, includeWorker bool) (any, error) {
	answerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		target *session
		serverOutcome
	}
	outcomes := make(chan answer)
	seen := make(map[int64]bool)
	for {
		if err := answerCtx.Err(); err != nil {
			return nil, err
		}
		r.mu.Lock()
		changed := r.interactionChanged
		r.mu.Unlock()
		for _, target := range r.interactiveTargets(threadID) {
			if seen[target.id] || (!includeWorker && target.role != RoleDesktop) {
				continue
			}
			seen[target.id] = true
			copyRequest := request
			if includeWorker && target.role == RoleDesktop {
				copyRequest.Params = withoutAutoResolution(request.Params)
			}
			go func() {
				result, err := target.invoke(answerCtx, copyRequest)
				select {
				case outcomes <- answer{target: target, serverOutcome: serverOutcome{result: result, err: err, role: target.role}}:
				case <-answerCtx.Done():
				}
			}()
		}
		select {
		case <-changed:
		case <-r.done:
			return nil, errSessionClosed
		case <-answerCtx.Done():
			return nil, answerCtx.Err()
		case outcome := <-outcomes:
			if err := answerCtx.Err(); err != nil {
				return nil, err
			}
			if outcome.err != nil {
				continue
			}
			r.mu.Lock()
			available := r.sessions[outcome.target.id] == outcome.target
			r.mu.Unlock()
			if !available || (outcome.role == RoleDesktop && threadID != "" && !outcome.target.subscribed(threadID)) {
				continue
			}
			if !includeWorker || r.options.Controller == nil {
				return outcome.result, nil
			}
			won, resolved, err := r.options.Controller.ResolveInteractive(answerCtx, request, outcome.result, outcome.role)
			if err == nil && won {
				return resolved, nil
			}
		}
	}
}

func (r *Hub) signalInteractionChange() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signalInteractionChangeLocked()
}

func (r *Hub) signalInteractionChangeLocked() {
	if r.interactionChanged != nil {
		close(r.interactionChanged)
	}
	r.interactionChanged = make(chan struct{})
}
