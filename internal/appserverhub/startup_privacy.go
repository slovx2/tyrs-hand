package appserverhub

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

func threadPrivacyFromMetadata(raw json.RawMessage) (bool, bool) {
	var value struct {
		Thread struct{ Ephemeral *bool }
	}
	if json.Unmarshal(raw, &value) != nil || value.Thread.Ephemeral == nil {
		return false, false
	}
	return *value.Thread.Ephemeral, true
}

type unclassifiedThreadEvent struct {
	event   codex.Event
	waits   []*pendingThreadStart
	expires time.Time
}

// false 也代表已明确分类为普通会话，不能将缺少元数据等同于 false。
func (r *Hub) classifyThreadPrivacy(threadID string, ephemeral bool) {
	if threadID == "" {
		return
	}
	r.mu.Lock()
	if r.ephemeralThreads == nil {
		r.ephemeralThreads = make(map[string]bool)
	}
	r.ephemeralThreads[threadID] = r.ephemeralThreads[threadID] || ephemeral
	r.mu.Unlock()
}

func (r *Hub) shouldQuarantine(event codex.Event, waits []*pendingThreadStart) bool {
	threadID, _ := threadScope(event.Params)
	if threadID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 同一会话不能绕过其已隔离的前序通知。
	for _, queued := range r.unclassifiedEvents {
		id, _ := threadScope(queued.event.Params)
		if id == threadID {
			return true
		}
	}
	if _, classified := r.ephemeralThreads[threadID]; classified {
		return false
	}
	if r.unclassifiedThreadGuard {
		return true
	}
	for _, wait := range waits {
		if wait.ephemeral {
			return true
		}
	}
	return false
}

func (r *Hub) quarantineThreadEvent(event codex.Event, waits []*pendingThreadStart) {
	r.mu.Lock()
	if len(r.unclassifiedEvents) >= r.options.EventBacklog {
		r.mu.Unlock()
		logUnclassifiedDrop(event, "capacity")
		return
	}
	r.unclassifiedEvents = append(r.unclassifiedEvents, unclassifiedThreadEvent{
		event: event, waits: waits, expires: time.Now().Add(r.options.RequestTimeout),
	})
	r.mu.Unlock()
}

func (r *Hub) forwardOrQuarantine(event codex.Event, waits []*pendingThreadStart) {
	if r.shouldQuarantine(event, waits) {
		if event.Method == "thread/closed" || event.Method == "thread/deleted" {
			logUnclassifiedDrop(event, "closed-before-classification")
			return
		}
		r.quarantineThreadEvent(event, waits)
		return
	}
	r.forwardEvent(event)
}

func (r *Hub) flushUnclassifiedEvents() {
	var ready, expired []codex.Event
	r.mu.Lock()
	remaining := r.unclassifiedEvents[:0]
	blocked := make(map[string]bool)
	for _, queued := range r.unclassifiedEvents {
		threadID, _ := threadScope(queued.event.Params)
		if !time.Now().Before(queued.expires) {
			expired = append(expired, queued.event)
			continue
		}
		_, classified := r.ephemeralThreads[threadID]
		resolved, matched := true, false
		for _, wait := range queued.waits {
			select {
			case <-wait.done:
				matched = matched || wait.threadID == threadID
			default:
				resolved = false
			}
		}
		if classified && (resolved || matched) && !blocked[threadID] {
			ready = append(ready, queued.event)
		} else {
			blocked[threadID] = true
			remaining = append(remaining, queued)
		}
	}
	clear(r.unclassifiedEvents[len(remaining):])
	r.unclassifiedEvents = remaining
	r.mu.Unlock()
	for _, event := range expired {
		logUnclassifiedDrop(event, "expired")
	}
	for _, event := range ready {
		r.forwardEvent(event)
	}
}

func (r *Hub) discardUnclassifiedEvents(threadID string) {
	var discarded []codex.Event
	r.mu.Lock()
	remaining := r.unclassifiedEvents[:0]
	for _, queued := range r.unclassifiedEvents {
		id, _ := threadScope(queued.event.Params)
		if id == threadID {
			discarded = append(discarded, queued.event)
		} else {
			remaining = append(remaining, queued)
		}
	}
	clear(r.unclassifiedEvents[len(remaining):])
	r.unclassifiedEvents = remaining
	r.mu.Unlock()
	for _, event := range discarded {
		logUnclassifiedDrop(event, "thread-closed")
	}
}

func logUnclassifiedDrop(event codex.Event, reason string) {
	threadID, _ := threadScope(event.Params)
	slog.Warn("丢弃无法确认隐私归属的会话事件", "method", event.Method, "thread_id", threadID, "reason", reason)
}
