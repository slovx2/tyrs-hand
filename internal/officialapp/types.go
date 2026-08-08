package officialapp

import "encoding/json"

type Thread struct {
	ID           string          `json:"id"`
	Preview      string          `json:"preview"`
	CWD          string          `json:"cwd"`
	Name         *string         `json:"name"`
	ThreadSource *string         `json:"threadSource"`
	Status       json.RawMessage `json:"status"`
	UpdatedAt    int64           `json:"updatedAt"`
	Turns        []Turn          `json:"turns"`
}

type Turn struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Items       []Item `json:"items"`
	StartedAt   *int64 `json:"startedAt"`
	CompletedAt *int64 `json:"completedAt"`
}

type Item struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	ClientID *string         `json:"clientId,omitempty"`
	Text     string          `json:"text,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

func (i *Item) UnmarshalJSON(data []byte) error {
	type alias Item
	var value alias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*i = Item(value)
	i.Raw = append(json.RawMessage(nil), data...)
	return nil
}

func (t Thread) LatestActiveTurn() *Turn {
	for index := len(t.Turns) - 1; index >= 0; index-- {
		if t.Turns[index].Status == "inProgress" {
			return &t.Turns[index]
		}
	}
	return nil
}

func (t Thread) FindClientMessage(clientID string) *Turn {
	for turnIndex := range t.Turns {
		for _, item := range t.Turns[turnIndex].Items {
			if item.Type == "userMessage" && item.ClientID != nil && *item.ClientID == clientID {
				return &t.Turns[turnIndex]
			}
		}
	}
	return nil
}

func (t Thread) LatestClientMessage() (turnID, clientID string) {
	for turnIndex := len(t.Turns) - 1; turnIndex >= 0; turnIndex-- {
		turn := t.Turns[turnIndex]
		for itemIndex := len(turn.Items) - 1; itemIndex >= 0; itemIndex-- {
			item := turn.Items[itemIndex]
			if item.Type == "userMessage" && item.ClientID != nil {
				return turn.ID, *item.ClientID
			}
		}
	}
	return "", ""
}

type Plan struct {
	TurnID string
	ItemID string
	Text   string
}

func (t Thread) LatestCompletedPlan() *Plan {
	for turnIndex := len(t.Turns) - 1; turnIndex >= 0; turnIndex-- {
		turn := t.Turns[turnIndex]
		if turn.Status != "completed" {
			continue
		}
		for itemIndex := len(turn.Items) - 1; itemIndex >= 0; itemIndex-- {
			item := turn.Items[itemIndex]
			if item.Type == "plan" {
				return &Plan{TurnID: turn.ID, ItemID: item.ID, Text: item.Text}
			}
		}
		return nil
	}
	return nil
}

type UserInput struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	TextElements []any  `json:"text_elements,omitempty"`
	Path         string `json:"path,omitempty"`
	Name         string `json:"name,omitempty"`
}

func TextInput(text string) UserInput {
	return UserInput{Type: "text", Text: text, TextElements: []any{}}
}

type Preferences struct {
	Model             string  `json:"model"`
	ReasoningEffort   *string `json:"reasoningEffort"`
	ServiceTier       *string `json:"serviceTier"`
	CollaborationMode string  `json:"collaborationMode"`
}
