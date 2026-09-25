package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 原生 thread 已创建且响应即将发给客户端时持久化。恢复只补登记映射，绝不再次创建原生 thread。
type threadRegistrationJournal struct {
	Engine   runtimeidentity.Engine                     `json:"engine"`
	Request  workerprotocol.DesktopThreadPrepareRequest `json:"request"`
	Response json.RawMessage                            `json:"response"`
}

var errInvalidThreadRegistration = errors.New("无效的 Thread 登记 Journal 身份，禁止自动重放")

func (s *journalStore) threadDirectory() string {
	return filepath.Join(filepath.Dir(s.directory), "threads")
}

func (s *journalStore) threadPath(workspaceID uuid.UUID, threadID string) string {
	hash := sha256.Sum256([]byte(workspaceID.String() + ":" + threadID))
	return filepath.Join(s.threadDirectory(), hex.EncodeToString(hash[:])+".json")
}

func (entry *threadRegistrationJournal) validate() error {
	if err := entry.Engine.Validate(); err != nil {
		return fmt.Errorf("%w: %v", errInvalidThreadRegistration, err)
	}
	threadID, _ := callScope(entry.Response)
	if entry.Request.WorkspaceID == uuid.Nil || entry.Request.RequestKey == "" || threadID == "" {
		return fmt.Errorf("%w: 缺少已确认的 Workspace、请求或原生 Thread 身份", errInvalidThreadRegistration)
	}
	if entry.Request.Operation != "start" && entry.Request.Operation != "fork" {
		return fmt.Errorf("%w: 未知原生会话操作", errInvalidThreadRegistration)
	}
	return nil
}

func (s *journalStore) saveThread(entry threadRegistrationJournal) error {
	if err := entry.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.threadDirectory(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	threadID, _ := callScope(entry.Response)
	return writeJournalFile(s.threadDirectory(), s.threadPath(entry.Request.WorkspaceID, threadID), data)
}

func (s *journalStore) readThread(path string) (*threadRegistrationJournal, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry threadRegistrationJournal
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidThreadRegistration, err)
	}
	if err := entry.validate(); err != nil {
		return nil, err
	}
	threadID, _ := callScope(entry.Response)
	if path != s.threadPath(entry.Request.WorkspaceID, threadID) {
		return nil, fmt.Errorf("%w: 文件名与身份不匹配", errInvalidThreadRegistration)
	}
	return &entry, nil
}

func (s *journalStore) loadThreads() ([]threadRegistrationJournal, error) {
	files, err := os.ReadDir(s.threadDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []threadRegistrationJournal
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		entry, err := s.readThread(filepath.Join(s.threadDirectory(), file.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取 Thread 登记 Journal: %w", err)
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}
	return entries, nil
}

func (s *journalStore) removeThread(entry threadRegistrationJournal) error {
	threadID, _ := callScope(entry.Response)
	err := os.Remove(s.threadPath(entry.Request.WorkspaceID, threadID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.threadDirectory())
}

func syncThreadRegistration(ctx context.Context, client *workerprotocol.Client, timeout time.Duration, entry threadRegistrationJournal) error {
	if client.Engine() != entry.Engine {
		return fmt.Errorf("%w: 与运行时引擎不匹配", errInvalidThreadRegistration)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	state, err := client.PrepareDesktopThread(requestCtx, entry.Request)
	cancel()
	if err != nil {
		return err
	}
	requestCtx, cancel = context.WithTimeout(ctx, timeout)
	_, err = client.CompleteDesktopThread(requestCtx, state.ID, workerprotocol.DesktopThreadCompleteRequest{
		WorkspaceID: entry.Request.WorkspaceID, Response: entry.Response,
	})
	cancel()
	return err
}

func (r *runtimeExecutor) restoreDesktopThread(ctx context.Context, request workerprotocol.DesktopTurnPrepareRequest) error {
	threadID, _ := callScope(request.Params)
	entry, err := r.journals.readThread(r.journals.threadPath(request.WorkspaceID, threadID))
	if err != nil || entry == nil {
		return err
	}
	if err := syncThreadRegistration(ctx, r.client, r.cfg.ControlTimeout, *entry); err != nil {
		return err
	}
	return r.journals.removeThread(*entry)
}
