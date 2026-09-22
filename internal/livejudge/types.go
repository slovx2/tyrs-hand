// Package livejudge 提供独立的语音委托判断与回放，不接入生产 Live。
package livejudge

import (
	"context"
	"time"
)

const Model = "jev-1.13.0"

type Utterance struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ContextMessage struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Source   string `json:"source,omitempty"`
	Text     string `json:"text"`
	Sequence int64  `json:"sequence"`
}

type Input struct {
	Background string           `json:"background"`
	Requests   []Utterance      `json:"voice_requests"`
	Previous   []string         `json:"previous_round,omitempty"`
	History    []ContextMessage `json:"recent_messages"`
	Notified   []string         `json:"already_notified"`
	Current    string           `json:"current_message"`
}

type Probabilities struct {
	Related    float64 `json:"related"`
	Notify     float64 `json:"notify"`
	Completed  float64 `json:"completed"`
	NeedsInput float64 `json:"needs_user_input"`
}

type Labels struct {
	Related    bool `json:"related"`
	Notify     bool `json:"notify"`
	Completed  bool `json:"completed"`
	NeedsInput bool `json:"needs_user_input"`
}

type Thresholds struct {
	Related    float64 `json:"related"`
	Notify     float64 `json:"notify"`
	Completed  float64 `json:"completed"`
	NeedsInput float64 `json:"needs_user_input"`
}

func (p Probabilities) Classify(t Thresholds) Labels {
	return Labels{p.Related >= t.Related, p.Notify >= t.Notify,
		p.Completed >= t.Completed, p.NeedsInput >= t.NeedsInput}
}

type Result struct {
	StartedAt     time.Time     `json:"started_at"`
	Probabilities Probabilities `json:"probabilities"`
	Model         string        `json:"model"`
	InputTokens   int           `json:"input_tokens"`
	OutputTokens  int           `json:"output_tokens"`
	Elapsed       time.Duration `json:"elapsed_ns"`
	RequestHash   string        `json:"request_hash"`
}

type Judge interface {
	Evaluate(context.Context, Input) (Result, error)
}

// Event 是回放与未来适配器共用的内部事件，不修改 Worker 协议。
type Event struct {
	Kind            string `json:"kind"`
	ID              string `json:"id,omitempty"`
	Run             string `json:"run,omitempty"`
	Turn            string `json:"turn,omitempty"`
	Sequence        int64  `json:"sequence,omitempty"`
	StartedSequence int64  `json:"started_sequence,omitempty"`
	Text            string `json:"text,omitempty"`
	Phase           string `json:"phase,omitempty"`
}

type Action struct {
	Called      bool   `json:"called"`
	Speak       bool   `json:"speak"`
	Closed      bool   `json:"closed"`
	Reason      string `json:"reason,omitempty"`
	FaultNotice bool   `json:"fault_notice"`
	Ignored     string `json:"ignored,omitempty"`
}

type Expected struct {
	Evaluate bool   `json:"evaluate"`
	Labels   Labels `json:"labels"`
	Speak    bool   `json:"speak"`
	Close    bool   `json:"close"`
	Critical bool   `json:"critical"`
}

type Step struct {
	Event    Event    `json:"event"`
	Expected Expected `json:"expected"`
}

type Scenario struct {
	ID         string `json:"id"`
	Family     string `json:"family"`
	Split      string `json:"split"`
	Background string `json:"background"`
	Steps      []Step `json:"steps"`
}
