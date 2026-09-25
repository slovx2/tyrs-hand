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

func (c *desktopController) startThreadRegistration(request workerprotocol.DesktopThreadPrepareRequest, result json.RawMessage) error {
	threadID, _ := callScope(result)
	key := request.WorkspaceID.String() + ":" + threadID
	if c.processor.journals == nil {
		return errors.New("Thread 登记缺少持久化目录")
	}
	entry := threadRegistrationJournal{Engine: c.processor.client.Engine(), Request: request, Response: append(json.RawMessage(nil), result...)}
	registration := &desktopThreadRegistration{done: make(chan struct{})}
	c.processor.threadSyncMu.Lock()
	if previous := c.processor.threadSync[key]; previous != nil {
		select {
		case <-previous.done:
			if previous.err != nil && !retryableControlError(previous.err) {
				c.processor.threadSyncMu.Unlock()
				return previous.err
			}
		default:
			c.processor.threadSyncMu.Unlock()
			return nil
		}
	}
	if err := c.processor.journals.saveThread(entry); err != nil {
		c.processor.threadSyncMu.Unlock()
		return err
	}
	if c.processor.threadSync == nil {
		c.processor.threadSync = make(map[string]*desktopThreadRegistration)
	}
	c.processor.threadSync[key] = registration
	c.processor.threadSyncMu.Unlock()
	go func() {
		registration.err = c.syncDesktopThread(request, result, nil)
		if registration.err == nil {
			registration.err = c.processor.journals.removeThread(entry)
		}
		c.processor.threadSyncMu.Lock()
		// 成功登记后不再需要屏障；失败保留原因，后续 Turn 不能把缺失映射当作新提交重放。
		if registration.err == nil && c.processor.threadSync[key] == registration {
			delete(c.processor.threadSync, key)
		}
		close(registration.done)
		c.processor.threadSyncMu.Unlock()
	}()
	return nil
}

func (c *desktopController) waitThreadRegistration(ctx context.Context, params json.RawMessage) error {
	threadID, _ := callScope(params)
	key := c.workspace.runtime.WorkspaceID.String() + ":" + threadID
	c.processor.threadSyncMu.Lock()
	registration := c.processor.threadSync[key]
	c.processor.threadSyncMu.Unlock()
	if registration == nil {
		if c.processor.journals == nil {
			return errors.New("会话登记缺少持久化目录")
		}
		entry, err := c.processor.journals.readThread(c.processor.journals.threadPath(c.workspace.runtime.WorkspaceID, threadID))
		if err != nil || entry == nil {
			return err
		}
		if entry.Engine != c.processor.client.Engine() {
			return errors.New("会话登记引擎与持久化身份不匹配")
		}
		if err := c.startThreadRegistration(entry.Request, entry.Response); err != nil {
			return err
		}
		return c.waitThreadRegistration(ctx, params)
	}
	select {
	case <-registration.done:
		return registration.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *desktopController) recoverThreadRegistrations() error {
	if c.processor.journals == nil {
		return errors.New("Thread 登记恢复缺少持久化目录")
	}
	entries, err := c.processor.journals.loadThreads()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Engine != c.processor.client.Engine() {
			return errInvalidThreadRegistration
		}
		if entry.Request.WorkspaceID != c.workspace.runtime.WorkspaceID {
			continue
		}
		if err := c.startThreadRegistration(entry.Request, entry.Response); err != nil {
			return err
		}
	}
	return nil
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

func (r *desktopEventReporter) confirmRegistration() {
	r.journal.mu.Lock()
	r.registrationConfirmed = true
	r.journal.mu.Unlock()
	r.finishRegistration()
}

// Control 工具和 Discord 交互依赖已提交的会话、Run、原生 Turn 映射。
func (r *desktopEventReporter) waitControlRegistration(ctx context.Context) error {
	r.journal.mu.Lock()
	done := r.registrationDone
	r.journal.mu.Unlock()
	if done == nil {
		return errors.New("Control 登记尚未启动")
	}
	select {
	case <-done:
		r.journal.mu.Lock()
		defer r.journal.mu.Unlock()
		if !r.registrationConfirmed || r.journal.ControlAbandoned {
			return errors.New("Control 登记未确认，不能执行依赖会话的工具或交互")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
