package worker

import (
	"context"
	"sync"
	"time"
)

// wakeSignals 汇集 Control 推送的唤醒种类，并提供“唤醒或兜底超时”的统一等待原语。
// 唤醒按种类合并，不排队：堆积多个待办时只触发一次拉取。
type wakeSignals struct {
	mu      sync.Mutex
	pending map[string]bool
	signal  chan struct{}
	state   wakeState
}

type wakeState struct {
	connected bool
	wake      bool
}

func newWakeSignals() *wakeSignals {
	return &wakeSignals{pending: make(map[string]bool), signal: make(chan struct{})}
}

// Notify 记录待处理的唤醒种类，并广播给所有等待者。
func (w *wakeSignals) Notify(kinds []string) {
	if w == nil || len(kinds) == 0 {
		return
	}
	w.mu.Lock()
	for _, kind := range kinds {
		if kind != "" {
			w.pending[kind] = true
		}
	}
	previous := w.signal
	w.signal = make(chan struct{})
	close(previous)
	w.mu.Unlock()
}

// SetState 记录控制通道的连接与唤醒能力状态。
func (w *wakeSignals) SetState(connected, wake bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.state = wakeState{connected: connected, wake: wake}
	w.mu.Unlock()
}

// WakeEnabled 表示当前 Control 通道已连接且双方都支持唤醒。
func (w *wakeSignals) WakeEnabled() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state.connected && w.state.wake
}

// Wait 等待任一关注种类被唤醒，或在兜底间隔到达后返回 true。
// 返回 false 表示 ctx 已取消，调用方应停止循环。
func (w *wakeSignals) Wait(ctx context.Context, fallback time.Duration, kinds ...string) bool {
	if fallback <= 0 {
		fallback = time.Minute
	}
	if w == nil {
		return waitContext(ctx, fallback)
	}
	if w.take(kinds) {
		return true
	}
	timer := time.NewTimer(fallback)
	defer timer.Stop()
	for {
		w.mu.Lock()
		signal := w.signal
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-signal:
			if w.take(kinds) {
				return true
			}
		}
	}
}

// take 消费关注的唤醒种类；未全部消费的种类留给对应循环处理。
func (w *wakeSignals) take(kinds []string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	matched := false
	for _, kind := range kinds {
		if w.pending[kind] {
			delete(w.pending, kind)
			matched = true
		}
	}
	return matched
}
