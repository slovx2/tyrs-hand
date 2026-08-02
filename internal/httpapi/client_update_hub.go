package httpapi

import "sync"

type clientUpdateHub struct {
	mu          sync.Mutex
	subscribers map[chan clientUpdate]struct{}
}

func newClientUpdateHub() *clientUpdateHub {
	return &clientUpdateHub{subscribers: make(map[chan clientUpdate]struct{})}
}

func (h *clientUpdateHub) subscribe() (<-chan clientUpdate, func()) {
	updates := make(chan clientUpdate, 64)
	h.mu.Lock()
	h.subscribers[updates] = struct{}{}
	h.mu.Unlock()
	return updates, func() {
		h.mu.Lock()
		if _, exists := h.subscribers[updates]; exists {
			delete(h.subscribers, updates)
			close(updates)
		}
		h.mu.Unlock()
	}
}

func (h *clientUpdateHub) publish(update clientUpdate) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		select {
		case subscriber <- update:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
}
