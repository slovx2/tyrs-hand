package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 入队前保存身份和本地 Run ID；原生自动执行不以 Control 在线为前提。
type hostQueueJournal struct {
	Version  int                               `json:"version"`
	Engine   runtimeidentity.Engine            `json:"engine"`
	ThreadID string                            `json:"threadId"`
	ClientID string                            `json:"clientUserMessageId"`
	Params   json.RawMessage                   `json:"params"`
	Manifest *workerprotocol.WorkspaceManifest `json:"manifest"`
	Task     *workerprotocol.Task              `json:"task,omitempty"`
	TurnID   string                            `json:"turnId,omitempty"`
}

// 输入以官方队列或 userMessage 为准；修改正文不能更换入队身份和 Run ID。
func (entry *hostQueueJournal) withInput(input json.RawMessage) (*hostQueueJournal, error) {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(input) == 0 || string(input) == "null" {
		return nil, errors.New("原生队列缺少实际输入")
	}
	if err := json.Unmarshal(input, &parts); err != nil {
		return nil, err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(entry.Params, &params); err != nil {
		return nil, err
	}
	params["input"] = append(json.RawMessage(nil), input...)
	copyEntry := *entry
	var err error
	copyEntry.Params, err = json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if entry.Task != nil {
		task := *entry.Task
		text := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				text = append(text, strings.TrimSpace(part.Text))
			}
		}
		task.Claimed.Instruction = strings.Join(text, "\n\n")
		if task.Snapshot.Session != nil {
			session := *task.Snapshot.Session
			session.Body = task.Claimed.Instruction
			task.Snapshot.Session = &session
		}
		copyEntry.Task = &task
	}
	return &copyEntry, nil
}

func (s *journalStore) queuePath(threadID, clientID string) string {
	digest := sha256.Sum256([]byte(threadID + "\x00" + clientID))
	return filepath.Join(filepath.Dir(s.directory), "queues", hex.EncodeToString(digest[:])+".json")
}

func (s *journalStore) saveQueue(entry *hostQueueJournal) error {
	if entry == nil || entry.Version != 1 || entry.Engine != runtimeidentity.Codex || entry.ThreadID == "" || entry.ClientID == "" {
		return errors.New("原生队列 Journal 身份无效")
	}
	if err := validateQueueAdmission(entry); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := s.queuePath(entry.ThreadID, entry.ClientID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeJournalFile(filepath.Dir(path), path, data)
}

func (s *journalStore) readQueue(threadID, clientID string) (*hostQueueJournal, error) {
	data, err := os.ReadFile(s.queuePath(threadID, clientID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry hostQueueJournal
	if json.Unmarshal(data, &entry) != nil || entry.Version != 1 || entry.Engine != runtimeidentity.Codex || entry.ThreadID != threadID || entry.ClientID != clientID {
		return nil, errors.New("原生队列 Journal 身份不匹配")
	}
	return &entry, nil
}

func (s *journalStore) removeQueue(threadID, clientID string) error {
	path := s.queuePath(threadID, clientID)
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (s *journalStore) loadQueues() ([]*hostQueueJournal, error) {
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(s.directory), "queues", "*.json"))
	if err != nil {
		return nil, err
	}
	entries := make([]*hostQueueJournal, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var entry hostQueueJournal
		if json.Unmarshal(data, &entry) != nil || entry.ThreadID == "" || entry.ClientID == "" || s.queuePath(entry.ThreadID, entry.ClientID) != path {
			return nil, errors.New("原生队列 Journal 文件身份无效")
		}
		validated, err := s.readQueue(entry.ThreadID, entry.ClientID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, validated)
	}
	return entries, nil
}

func (c *HostDesktopController) captureQueueInput(ctx context.Context, runtime *hostworker.Runtime,
	integration *desktopController, call appserverhub.Call, item *hostQueueItem,
) (json.RawMessage, error) {
	params := participantidentity.StripTurnContext(call.Params)
	threadID, _ := callScope(params)
	var thread struct {
		Thread struct{ CWD string } `json:"thread"`
	}
	client := runtime.Client()
	if client == nil {
		return nil, errors.New("宿主 Codex Runtime 正在恢复")
	}
	if err := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &thread); err != nil {
		return nil, err
	}
	var local map[string]json.RawMessage
	if err := json.Unmarshal(params, &local); err != nil {
		return nil, err
	}
	local["cwd"], _ = json.Marshal(thread.Thread.CWD)
	localParams, err := json.Marshal(local)
	if err != nil {
		return nil, err
	}
	entry := &hostQueueJournal{Version: 1, Engine: runtimeidentity.Codex, ThreadID: threadID, ClientID: item.ClientID, Params: localParams}
	if integration != nil {
		// queue/add 原生 schema 没有 additionalContext；身份仅保存于本地授权快照。
		if owner, ok := integration.workspace.ownerParticipant(); ok {
			entry.Params = participantidentity.InjectTurnContext(localParams, participantidentity.Participant{ID: owner.ParticipantID, DisplayName: owner.DisplayName})
		}
		manifest := integration.workspace.manifest
		entry.Manifest = &manifest
		task, _, taskErr := integration.localDesktopTask(entry.Params)
		if taskErr != nil {
			return nil, taskErr
		}
		entry.Task = &task
	}
	if err := c.processor.journals.saveQueue(entry); err != nil {
		return nil, err
	}
	c.mu.Lock()
	item.journal, item.controller = entry, integration
	c.mu.Unlock()
	return params, nil
}

// 恢复只接受入队时的身份快照；未登记的原生条目不能自动认领当前 Workspace。
func (c *HostDesktopController) queueController(entry *hostQueueJournal, runtime *hostworker.Runtime) *desktopController {
	if entry == nil || entry.Manifest == nil {
		return nil
	}
	current, _ := c.snapshot()
	if current != nil && reflect.DeepEqual(current.workspace.manifest, *entry.Manifest) {
		return current
	}
	workspace := &workspaceCodex{manifest: *entry.Manifest, processor: c.processor, hostRuntime: runtime,
		runtime: workspaceRuntime{WorkspaceID: entry.Manifest.WorkspaceID}, client: runtime.Client(), generation: runtime.Generation()}
	return &desktopController{processor: c.processor, workspace: workspace, controlValid: func() bool { return false }}
}
