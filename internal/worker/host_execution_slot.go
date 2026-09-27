package worker

import (
	"errors"
	"sync"
)

// 同一线程的当前回合与原生队列共用一个槽；最后一个持有者结束时才归还。
type hostExecutionSlot struct {
	mu        sync.Mutex
	users     int
	onRelease func()
}

func (s *hostExecutionSlot) retain() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == 0 {
		return false
	}
	s.users++
	return true
}

func (s *hostExecutionSlot) release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users > 0 {
		s.users--
		if s.users == 0 && s.onRelease != nil {
			s.onRelease()
		}
	}
}

// 调用方持有 Controller.mu；两个引擎最终都使用 Processor 的同一个 semaphore。
func (c *HostDesktopController) reserveExecutionSlot(threadID string) (*hostExecutionSlot, error) {
	if active := c.active[threadID]; active != nil && active.slot.retain() {
		return active.slot, nil
	}
	if queued := c.queued[threadID]; queued != nil && queued.slot.retain() {
		return queued.slot, nil
	}
	slot := &hostExecutionSlot{users: 1}
	if c.processor.turnSlots != nil {
		select {
		case c.processor.turnSlots <- struct{}{}:
			slot.onRelease = func() { <-c.processor.turnSlots }
		default:
			return nil, errors.New("已达到 Worker 两个引擎共享的并发上限")
		}
	}
	return slot, nil
}
