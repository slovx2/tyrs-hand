package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (p *Processor) applyPendingThreadLifecycles(ctx context.Context) error {
	updates, err := p.client.PendingThreadLifecycles(ctx)
	if err != nil {
		return fmt.Errorf("读取待应用 Thread lifecycle: %w", err)
	}
	var failures []error
	for _, update := range updates {
		entry := p.workspaces.get(update.WorkspaceID)
		if entry == nil {
			continue
		}
		method := "thread/unarchive"
		if update.DesiredState == "archived" {
			method = "thread/archive"
		}
		var response json.RawMessage
		requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		callErr := entry.client.Call(requestCtx, method,
			map[string]any{"threadId": update.ThreadID}, &response)
		cancel()
		ack := workerprotocol.ThreadLifecycleCompleteRequest{
			WorkspaceID: update.WorkspaceID, Response: response}
		if callErr != nil {
			ack.Error = callErr.Error()
			failures = append(failures, fmt.Errorf("应用 Thread %s lifecycle: %w",
				update.ThreadID, callErr))
		}
		ackCtx, ackCancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		ackErr := p.client.CompleteThreadLifecycle(ackCtx, update.ID, ack)
		ackCancel()
		if ackErr != nil {
			failures = append(failures, fmt.Errorf("确认 Thread %s lifecycle: %w",
				update.ThreadID, ackErr))
		}
	}
	return errors.Join(failures...)
}

func (p *Processor) applyPendingThreadNames(ctx context.Context) error {
	updates, err := p.client.PendingThreadNames(ctx)
	if err != nil {
		return fmt.Errorf("读取待应用 Thread 标题: %w", err)
	}
	var failures []error
	for _, update := range updates {
		entry := p.workspaces.get(update.WorkspaceID)
		if entry == nil {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		callErr := entry.client.Call(requestCtx, "thread/name/set",
			map[string]any{"threadId": update.ThreadID, "name": update.Name}, nil)
		cancel()
		ack := workerprotocol.ThreadNameAckRequest{WorkspaceID: update.WorkspaceID,
			Revision: update.Revision}
		if callErr != nil {
			ack.Error = callErr.Error()
			failures = append(failures, fmt.Errorf("应用 Thread %s 标题: %w",
				update.ThreadID, callErr))
		}
		ackCtx, ackCancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		ackErr := p.client.AckThreadName(ackCtx, update.ControlID, ack)
		ackCancel()
		if ackErr != nil {
			failures = append(failures, fmt.Errorf("确认 Thread %s 标题: %w",
				update.ThreadID, ackErr))
		}
	}
	return errors.Join(failures...)
}
