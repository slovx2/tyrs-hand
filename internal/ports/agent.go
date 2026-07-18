package ports

import (
	"context"
	"encoding/json"
)

type DynamicToolSpec struct {
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	InputSchema  json.RawMessage   `json:"inputSchema,omitempty"`
	DeferLoading bool              `json:"deferLoading,omitempty"`
	Tools        []DynamicToolSpec `json:"tools,omitempty"`
}

type ThreadOptions struct {
	CWD                   string
	Model                 string
	ReasoningEffort       string
	ServiceTier           string
	Sandbox               string
	ApprovalPolicy        string
	NetworkEnabled        bool
	BaseInstructions      string
	DeveloperInstructions string
	DynamicTools          []DynamicToolSpec
}

type TurnInput struct {
	Text                string
	ClientUserMessageID string
	LocalImages         []LocalImageInput
	AdditionalContext   map[string]AdditionalContextEntry
	Skills              []SkillRef
	OutputSchema        json.RawMessage
}

type LocalImageInput struct {
	Path   string
	Detail string
}

type AdditionalContextEntry struct {
	Value string
	Kind  string
}

type SkillRef struct {
	Name string
	Path string
}

type AgentRuntime interface {
	StartThread(ctx context.Context, options ThreadOptions) (string, error)
	ResumeThread(ctx context.Context, threadID string, options ThreadOptions) error
	StartTurn(ctx context.Context, threadID string, input TurnInput) (string, error)
	SteerTurn(ctx context.Context, threadID, turnID string, input TurnInput) error
	InterruptTurn(ctx context.Context, threadID, turnID string) error
}
