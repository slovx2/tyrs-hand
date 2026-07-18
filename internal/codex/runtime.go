package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/ports"
)

type Runtime struct {
	client *Client
}

func NewRuntime(client *Client) *Runtime { return &Runtime{client: client} }

func (r *Runtime) StartThread(ctx context.Context, options ports.ThreadOptions) (string, error) {
	payload := threadPayload(options)
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := r.client.Call(ctx, "thread/start", payload, &result); err != nil {
		return "", err
	}
	if result.Thread.ID == "" {
		return "", errors.New("调用 Codex thread/start 未返回 Thread ID")
	}
	return result.Thread.ID, nil
}

func (r *Runtime) ResumeThread(ctx context.Context, threadID string, options ports.ThreadOptions) error {
	payload := threadPayload(options)
	delete(payload, "dynamicTools")
	payload["threadId"] = threadID
	return r.client.Call(ctx, "thread/resume", payload, nil)
}

func (r *Runtime) StartTurn(ctx context.Context, threadID string, input ports.TurnInput) (string, error) {
	items := userInput(input)
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	payload := map[string]any{
		"threadId": threadID, "clientUserMessageId": input.ClientUserMessageID, "input": items,
	}
	addTurnContext(payload, input.AdditionalContext)
	if len(input.OutputSchema) > 0 {
		payload["outputSchema"] = input.OutputSchema
	}
	err := r.client.Call(ctx, "turn/start", payload, &result)
	if err != nil {
		return "", err
	}
	if result.Turn.ID == "" {
		return "", errors.New("调用 Codex turn/start 未返回 Turn ID")
	}
	return result.Turn.ID, nil
}

func (r *Runtime) SteerTurn(ctx context.Context, threadID, turnID string, input ports.TurnInput) error {
	payload := map[string]any{
		"threadId": threadID, "expectedTurnId": turnID,
		"clientUserMessageId": input.ClientUserMessageID, "input": userInput(input),
	}
	addTurnContext(payload, input.AdditionalContext)
	return r.client.Call(ctx, "turn/steer", payload, nil)
}

func (r *Runtime) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	return r.client.Call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}

func (r *Runtime) ValidateSkills(ctx context.Context, cwd string, expected []ports.SkillRef) error {
	var result struct {
		Data []struct {
			CWD    string `json:"cwd"`
			Skills []struct {
				Name    string `json:"name"`
				Path    string `json:"path"`
				Enabled bool   `json:"enabled"`
			} `json:"skills"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"data"`
	}
	if err := r.client.Call(ctx, "skills/list", map[string]any{"cwds": []string{absolute(cwd)}, "forceReload": true}, &result); err != nil {
		return err
	}
	found := make(map[string]string)
	for _, entry := range result.Data {
		for _, skill := range entry.Skills {
			if skill.Enabled {
				found[skill.Name] = canonicalPath(skill.Path)
			}
		}
	}
	for _, skill := range expected {
		path, ok := found[skill.Name]
		if !ok || path != canonicalPath(skill.Path) {
			return fmt.Errorf("仓库 Skill %s 未被 Codex 正确发现", skill.Name)
		}
	}
	return nil
}

func canonicalPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func threadPayload(options ports.ThreadOptions) map[string]any {
	config := map[string]any{
		"sandbox_workspace_write": map[string]any{"network_access": options.NetworkEnabled},
		"features":                map[string]any{"unified_exec": true, "memory_tool": false},
	}
	payload := map[string]any{
		"cwd": absolute(options.CWD), "runtimeWorkspaceRoots": []string{absolute(options.CWD)},
		"approvalPolicy": options.ApprovalPolicy, "sandbox": options.Sandbox,
		"config": config, "dynamicTools": options.DynamicTools,
	}
	optional(payload, "model", options.Model)
	optional(payload, "effort", options.ReasoningEffort)
	optional(payload, "serviceTier", options.ServiceTier)
	optional(payload, "baseInstructions", options.BaseInstructions)
	optional(payload, "developerInstructions", options.DeveloperInstructions)
	return payload
}

func userInput(input ports.TurnInput) []map[string]any {
	text := input.Text
	for _, skill := range input.Skills {
		text = "$" + skill.Name + "\n" + text
	}
	items := []map[string]any{{"type": "text", "text": text, "textElements": []any{}}}
	for _, image := range input.LocalImages {
		item := map[string]any{"type": "localImage", "path": absolute(image.Path)}
		optional(item, "detail", image.Detail)
		items = append(items, item)
	}
	for _, skill := range input.Skills {
		items = append(items, map[string]any{"type": "skill", "name": skill.Name, "path": absolute(skill.Path)})
	}
	return items
}

func addTurnContext(payload map[string]any, context map[string]ports.AdditionalContextEntry) {
	if len(context) == 0 {
		return
	}
	entries := make(map[string]map[string]string, len(context))
	for key, entry := range context {
		entries[key] = map[string]string{"value": entry.Value, "kind": entry.Kind}
	}
	payload["additionalContext"] = entries
}

func optional(payload map[string]any, key, value string) {
	if value != "" {
		payload[key] = value
	}
}
