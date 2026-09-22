package livejudge

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// Tracker 由每条委托的串行队列驱动。网络请求在 Prepare/Commit 之间执行。
// 新委托、转接、挂断会令旧票据失效；Busy 时调用方应保留事件并稍后重试。
type Tracker struct {
	mu    sync.Mutex
	State Snapshot
}
type Snapshot struct {
	Background             string
	Generation             uint64
	Active, Pending, Fault bool
	Run, Turn              string
	Boundary, LastSequence int64
	Requests               []Utterance
	Previous, Notified     []string
	History                []ContextMessage
	Seen                   map[string]bool
	InFlight               string
}
type Ticket struct {
	Generation uint64
	Key        string
	Event      Event
	Input      Input
}

func NewTracker(background string) *Tracker {
	return &Tracker{State: Snapshot{Background: background, Seen: map[string]bool{}}}
}

func (t *Tracker) Save() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, _ := json.Marshal(t.State)
	return b
}
func Restore(data []byte) (*Tracker, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	// 崩溃时尚未提交的消息可以安全重新判断。
	delete(s.Seen, s.InFlight)
	s.InFlight = ""
	s.Generation++
	if s.Seen == nil {
		s.Seen = map[string]bool{}
	}
	return &Tracker{State: s}, nil
}

func (t *Tracker) Prepare(e Event) (*Ticket, Action) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.State
	switch e.Kind {
	case "voice":
		if e.ID == "" || strings.TrimSpace(e.Text) == "" {
			return nil, Action{Ignored: "invalid_voice"}
		}
		key := "voice/" + e.ID
		if s.Seen[key] {
			return nil, Action{Ignored: "duplicate"}
		}
		s.Seen[key] = true
		if !s.Active && !s.Pending {
			s.Previous = trim(append(requestTexts(s.Requests), contextTexts(s.History)...), 4000)
			s.Requests = nil
			s.History = nil
			s.Notified = nil
		}
		s.Requests = append(s.Requests, Utterance{e.ID, e.Text})
		s.History = trimMessages(append(s.History, ContextMessage{e.ID, "user", "voice", e.Text, e.Sequence}), 6000)
		s.Generation++
		s.InFlight = ""
		s.Pending = true
		s.Active = false
		s.Fault = false
		return nil, Action{}
	case "applied":
		if e.Run == "" || e.Turn == "" {
			return nil, Action{Ignored: "invalid_binding"}
		}
		if !s.Pending || len(s.Requests) == 0 || e.ID != s.Requests[len(s.Requests)-1].ID {
			return nil, Action{Ignored: "stale_application"}
		}
		s.Run, s.Turn, s.Boundary, s.LastSequence = e.Run, e.Turn, e.Sequence, e.Sequence
		s.Active = true
		s.Pending = false
		return nil, Action{}
	case "transfer", "hangup":
		was := s.Active || s.Pending
		s.Active = false
		s.Pending = false
		s.Generation++
		s.InFlight = ""
		if e.Kind == "transfer" {
			s.Run = ""
			s.Turn = ""
			s.Requests = nil
			s.Previous = nil
			s.History = nil
			s.Notified = nil
			s.Background = e.Text
		}
		return nil, Action{Closed: was, Reason: e.Kind}
	}
	if (!s.Active || s.Pending) && e.Kind != "user" {
		return nil, Action{Ignored: "inactive"}
	}
	if e.Run != s.Run || e.Turn != s.Turn {
		return nil, Action{Ignored: "other_turn"}
	}
	if e.Sequence <= s.Boundary || (e.StartedSequence > 0 && e.StartedSequence <= s.Boundary) {
		return nil, Action{Ignored: "before_input"}
	}
	if e.Kind != "message" && e.Kind != "user" && e.Kind != "turn_end" {
		return nil, Action{Ignored: "not_complete_message"}
	}
	key := fmt.Sprintf("%s/%s/%s", e.Run, e.Turn, e.ID)
	if s.Seen[key] {
		return nil, Action{Ignored: "duplicate"}
	}
	if s.InFlight != "" {
		return nil, Action{Ignored: "busy"}
	}
	if e.Sequence <= s.LastSequence {
		return nil, Action{Ignored: "out_of_order"}
	}
	if e.Kind == "user" {
		if e.ID == "" || strings.TrimSpace(e.Text) == "" {
			return nil, Action{Ignored: "empty_message"}
		}
		s.Seen[key] = true
		s.LastSequence = e.Sequence
		s.History = trimMessages(append(s.History, ContextMessage{e.ID, "user", "text", e.Text, e.Sequence}), 6000)
		return nil, Action{}
	}
	if e.Kind == "turn_end" {
		s.LastSequence = e.Sequence
		s.Active = false
		s.Generation++
		return nil, Action{Closed: true, Reason: "turn_end"}
	}
	if strings.TrimSpace(e.Text) == "" && e.Phase == "final_answer" {
		s.Active = false
		s.Generation++
		return nil, Action{Closed: true, Reason: "final_answer"}
	}
	if e.ID == "" || strings.TrimSpace(e.Text) == "" {
		return nil, Action{Ignored: "empty_message"}
	}
	s.Seen[key] = true
	s.InFlight = key
	input := Input{s.Background, append([]Utterance(nil), s.Requests...), append([]string(nil), s.Previous...), trimMessages(s.History, 6000), trim(s.Notified, 4000), e.Text}
	return &Ticket{s.Generation, key, e, input}, Action{Called: true}
}

func (t *Tracker) Commit(ticket *Ticket, labels Labels, err error) Action {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.State
	if ticket.Generation != s.Generation || ticket.Key != s.InFlight {
		return Action{Ignored: "stale_result"}
	}
	s.InFlight = ""
	s.LastSequence = ticket.Event.Sequence
	a := Action{Called: true}
	if err != nil {
		a.FaultNotice = !s.Fault
		s.Fault = true
	} else {
		s.Fault = false
		a.Speak = labels.Related && (labels.Notify || labels.Completed || labels.NeedsInput)
		if a.Speak {
			s.Notified = trim(append(s.Notified, ticket.Event.Text), 4000)
		}
		if labels.Related && (labels.Completed || labels.NeedsInput) {
			a.Closed = true
			a.Reason = "completed"
			if labels.NeedsInput {
				a.Reason = "needs_user_input"
			}
		}
	}
	s.History = trimMessages(append(s.History, ContextMessage{ticket.Event.ID, "assistant", "", ticket.Event.Text, ticket.Event.Sequence}), 6000)
	if ticket.Event.Phase == "final_answer" && !a.Closed {
		a.Closed = true
		a.Reason = "final_answer"
	}
	if a.Closed {
		s.Active = false
		s.Generation++
	}
	return a
}

func requestTexts(r []Utterance) []string {
	out := make([]string, 0, len(r))
	for _, v := range r {
		out = append(out, v.Text)
	}
	return out
}

// 按时间丢弃最旧整条历史；当前完整消息和委托原文不截断。
func trim(s []string, budget int) []string {
	n := 0
	start := len(s)
	for start > 0 {
		size := utf8.RuneCountInString(s[start-1])
		if n+size > budget {
			break
		}
		n += size
		start--
	}
	return append([]string(nil), s[start:]...)
}

func trimMessages(s []ContextMessage, budget int) []ContextMessage {
	n := 0
	start := len(s)
	for start > 0 {
		size := utf8.RuneCountInString(s[start-1].Text)
		if n+size > budget {
			break
		}
		n += size
		start--
	}
	return append([]ContextMessage(nil), s[start:]...)
}

func contextTexts(messages []ContextMessage) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, m.Role+": "+m.Text)
	}
	return out
}
