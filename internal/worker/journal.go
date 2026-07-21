package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type runJournal struct {
	Task          workerprotocol.Task         `json:"task"`
	NextSequence  int64                       `json:"nextSequence"`
	PendingEvents []workerprotocol.EventInput `json:"pendingEvents,omitempty"`
	Result        *codexcontrol.TurnResult    `json:"result,omitempty"`
	FailureCode   string                      `json:"failureCode,omitempty"`
	Failure       string                      `json:"failure,omitempty"`
}

type journalStore struct {
	directory string
}

func newJournalStore(workerRoot string) (*journalStore, error) {
	directory := filepath.Join(workerRoot, "control-state", "runs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(directory), 0o700); err != nil {
		return nil, err
	}
	return &journalStore{directory: directory}, nil
}

func (s *journalStore) path(runID uuid.UUID) string {
	return filepath.Join(s.directory, runID.String()+".json")
}

func (s *journalStore) save(journal *runJournal) error {
	if journal == nil || journal.Task.Claimed.RunID == uuid.Nil {
		return errors.New("run Journal 缺少任务或 Run ID")
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.directory, ".journal-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path(journal.Task.Claimed.RunID)); err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

func (s *journalStore) remove(runID uuid.UUID) error {
	err := os.Remove(s.path(runID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.directory)
}

func (s *journalStore) loadAll() ([]*runJournal, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]*runJournal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var journal runJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("读取 Run Journal %s: %w", entry.Name(), err)
		}
		if journal.NextSequence <= 0 {
			journal.NextSequence = 1
		}
		result = append(result, &journal)
	}
	return result, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

type workerDataLock struct {
	file *os.File
}

func acquireWorkerDataLock(root string) (*workerDataLock, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "worker.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("同一数据目录已经有 Worker 实例运行")
	}
	return &workerDataLock{file: file}, nil
}

func (l *workerDataLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
