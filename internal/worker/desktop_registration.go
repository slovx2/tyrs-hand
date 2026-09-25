package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type desktopThreadRegistration struct {
	done chan struct{}
	err  error
}

func (c *desktopController) startThreadRegistration(request workerprotocol.DesktopThreadPrepareRequest, result json.RawMessage) {
	threadID, _ := callScope(result)
	key := request.WorkspaceID.String() + ":" + threadID
	registration := &desktopThreadRegistration{done: make(chan struct{})}
	c.processor.threadSyncMu.Lock()
	if c.processor.threadSync == nil {
		c.processor.threadSync = make(map[string]*desktopThreadRegistration)
	}
	c.processor.threadSync[key] = registration
	c.processor.threadSyncMu.Unlock()
	go func() {
		registration.err = c.syncDesktopThread(request, result, nil)
		c.processor.threadSyncMu.Lock()
		// 成功登记后不再需要屏障；失败保留原因，后续 Turn 不能把缺失映射当作新提交重放。
		if registration.err == nil && c.processor.threadSync[key] == registration {
			delete(c.processor.threadSync, key)
		}
		close(registration.done)
		c.processor.threadSyncMu.Unlock()
	}()
}

func (c *desktopController) waitThreadRegistration(ctx context.Context, params json.RawMessage) error {
	threadID, _ := callScope(params)
	key := c.workspace.runtime.WorkspaceID.String() + ":" + threadID
	c.processor.threadSyncMu.Lock()
	registration := c.processor.threadSync[key]
	c.processor.threadSyncMu.Unlock()
	if registration == nil {
		return nil
	}
	select {
	case <-registration.done:
		return registration.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *desktopEventReporter) holdRegistration() {
	r.journal.mu.Lock()
	defer r.journal.mu.Unlock()
	r.registrationDone = make(chan struct{})
}

func (r *desktopEventReporter) finishRegistration() {
	r.journal.mu.Lock()
	defer r.journal.mu.Unlock()
	if r.registrationDone != nil {
		select {
		case <-r.registrationDone:
		default:
			close(r.registrationDone)
		}
	}
}

func (r *desktopEventReporter) registrationPendingLocked() bool {
	if r.registrationDone == nil {
		return false
	}
	select {
	case <-r.registrationDone:
		return false
	default:
		return true
	}
}

var errDesktopBindingChanged = errors.New("会话登记期间 Worker Workspace 绑定已改变")
