package worker

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type workspaceCodexRegistry struct {
	ctx       context.Context
	processor *Processor

	mu      sync.Mutex
	entries map[uuid.UUID]*workspaceCodex
}

type workspaceCodex struct {
	client      *appserverhub.Client
	manifest    workerprotocol.WorkspaceManifest
	runtime     workspaceRuntime
	generation  int64
	processor   *Processor
	hostRuntime *hostworker.Runtime

	mu               sync.Mutex
	metadataEvents   *appserverhub.Subscription
	metadataSequence atomic.Int64
	settingsSequence atomic.Int64
	modelCatalog     json.RawMessage
}

type workspaceRuntime struct {
	WorkspaceID uuid.UUID
}

func newWorkspaceCodexRegistry(ctx context.Context, processor *Processor) *workspaceCodexRegistry {
	registry := &workspaceCodexRegistry{ctx: ctx, processor: processor,
		entries: make(map[uuid.UUID]*workspaceCodex)}
	go func() {
		<-ctx.Done()
		registry.close()
	}()
	return registry
}

func (e *workspaceCodex) ownerParticipant() (workerprotocol.ParticipantIdentity, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.manifest.OwnerParticipant == nil {
		return workerprotocol.ParticipantIdentity{}, false
	}
	return *e.manifest.OwnerParticipant, true
}

func (r *workspaceCodexRegistry) get(workspaceID uuid.UUID) *workspaceCodex {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[workspaceID]
}

func (r *workspaceCodexRegistry) modelCatalogs() map[string]json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]json.RawMessage, len(r.entries))
	for workspaceID, entry := range r.entries {
		entry.mu.Lock()
		catalog := append(json.RawMessage(nil), entry.modelCatalog...)
		entry.mu.Unlock()
		if len(catalog) > 0 {
			result[workspaceID.String()] = catalog
		}
	}
	return result
}

func (e *workspaceCodex) observeMetadata(ctx context.Context) {
	for {
		select {
		case event, ok := <-e.metadataEvents.Events():
			if !ok {
				return
			}
			switch event.Method {
			case "thread/name/updated":
				var value struct {
					ThreadID   string `json:"threadId"`
					ThreadName string `json:"threadName"`
				}
				if json.Unmarshal(event.Params, &value) == nil {
					e.recordThreadName(ctx, value.ThreadID, value.ThreadName)
				}
			case "thread/archived", "thread/unarchived":
				var value struct {
					ThreadID string `json:"threadId"`
				}
				if json.Unmarshal(event.Params, &value) == nil {
					state := "archived"
					if event.Method == "thread/unarchived" {
						state = "active"
					}
					e.recordThreadLifecycle(ctx, value.ThreadID, state)
				}
			case "thread/settings/updated":
				var value struct {
					ThreadID       string `json:"threadId"`
					ThreadSettings struct {
						Model             string `json:"model"`
						ServiceTier       string `json:"serviceTier"`
						Effort            string `json:"effort"`
						CollaborationMode struct {
							Mode string `json:"mode"`
						} `json:"collaborationMode"`
					} `json:"threadSettings"`
				}
				if json.Unmarshal(event.Params, &value) == nil {
					e.recordThreadSettings(ctx, value.ThreadID, value.ThreadSettings.Model,
						value.ThreadSettings.Effort, value.ThreadSettings.ServiceTier,
						value.ThreadSettings.CollaborationMode.Mode, "app_server")
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (e *workspaceCodex) recordThreadName(ctx context.Context, threadID, name string) {
	if e.processor == nil || threadID == "" || name == "" {
		return
	}
	event := workerprotocol.ThreadMetadataEvent{ThreadID: threadID,
		Sequence: e.metadataSequence.Add(1), Kind: "name", Name: name}
	e.recordThreadMetadata(ctx, event)
}

func (e *workspaceCodex) recordThreadLifecycle(ctx context.Context, threadID, state string) {
	if e.processor == nil || threadID == "" || (state != "active" && state != "archived") {
		return
	}
	event := workerprotocol.ThreadMetadataEvent{ThreadID: threadID,
		Sequence: e.metadataSequence.Add(1), Kind: "lifecycle", LifecycleState: state}
	e.recordThreadMetadata(ctx, event)
}

func (e *workspaceCodex) recordThreadSettings(ctx context.Context, threadID, model,
	effort, tier, collaborationMode, source string,
) {
	if e.processor == nil || threadID == "" || (model == "" && collaborationMode == "") {
		return
	}
	event := workerprotocol.ThreadMetadataEvent{ThreadID: threadID,
		Sequence: e.settingsSequence.Add(1), Kind: "settings", Source: source, Model: model,
		ReasoningEffort: effort, ServiceTier: tier, CollaborationMode: collaborationMode}
	e.recordThreadMetadata(ctx, event)
}

func (e *workspaceCodex) recordThreadMetadata(ctx context.Context,
	event workerprotocol.ThreadMetadataEvent,
) {
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, e.processor.cfg.ControlTimeout)
		err := e.processor.client.RecordThreadMetadata(requestCtx,
			workerprotocol.ThreadMetadataRequest{
				WorkspaceID: e.runtime.WorkspaceID, Generation: e.generation,
				Events: []workerprotocol.ThreadMetadataEvent{event},
			})
		cancel()
		if err == nil {
			return
		}
		e.processor.logger.Warn("提交 Codex Thread metadata 失败",
			zap.String("thread_id", event.ThreadID), zap.String("kind", event.Kind),
			zap.Error(err))
		if !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
}

func (e *workspaceCodex) reconcileThreadLifecycles(ctx context.Context) {
	for _, archived := range []bool{false, true} {
		var cursor *string
		for ctx.Err() == nil {
			var result struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				NextCursor *string `json:"nextCursor"`
			}
			params := threadLifecycleListParams(archived, cursor)
			requestCtx, cancel := context.WithTimeout(ctx, e.processor.cfg.ControlTimeout)
			err := e.client.Call(requestCtx, "thread/list", params, &result)
			cancel()
			if err != nil {
				e.processor.logger.Warn("对账 Codex Thread lifecycle 失败",
					zap.Bool("archived", archived), zap.Error(err))
				return
			}
			state := "active"
			if archived {
				state = "archived"
			}
			for _, thread := range result.Data {
				e.recordThreadLifecycle(ctx, thread.ID, state)
			}
			if result.NextCursor == nil || *result.NextCursor == "" {
				break
			}
			cursor = result.NextCursor
		}
	}
}

func threadLifecycleListParams(archived bool, cursor *string) map[string]any {
	params := map[string]any{
		"archived": archived, "limit": 100, "modelProviders": []string{},
	}
	if cursor != nil && *cursor != "" {
		params["cursor"] = *cursor
	}
	return params
}

func (e *workspaceCodex) bindTool(threadID string, handler codex.ToolHandler) func() {
	return e.hostRuntime.BindTool(threadID, handler)
}

func (e *workspaceCodex) bindInteractive(threadID string,
	handler codex.ServerRequestHandler,
) func() {
	return e.hostRuntime.BindInteractive(threadID, handler)
}

func serverRequestScope(raw json.RawMessage) (string, string, string) {
	var value struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.ThreadID, value.TurnID, value.ItemID
}

func (r *workspaceCodexRegistry) close() {
	r.mu.Lock()
	entries := make([]*workspaceCodex, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.entries = make(map[uuid.UUID]*workspaceCodex)
	r.mu.Unlock()
	for _, entry := range entries {
		if entry.metadataEvents != nil {
			entry.metadataEvents.Close()
		}
	}
}
