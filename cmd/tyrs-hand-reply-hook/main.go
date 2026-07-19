package main

import (
	"encoding/json"
	"os"

	"github.com/slovx2/tyrs-hand/internal/replygate"
)

func main() {
	approve := func() {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"decision": "approve", "suppressOutput": true})
	}
	var payload struct {
		EventName string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
	}
	if json.NewDecoder(os.Stdin).Decode(&payload) != nil || payload.EventName != "Stop" || payload.SessionID == "" {
		approve()
		return
	}
	home := os.Getenv("CODEX_HOME")
	decision := replygate.Evaluate(home, payload.SessionID)
	if !decision.Block {
		approve()
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"decision": "block",
		"reason":   decision.Reason,
	})
}
