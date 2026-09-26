package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// MigrateCodexJournalRuntime 只用于升级前的 Codex 状态目录，在持有 Worker 数据锁时调用。
// 标记保留首次迁移语义；回滚旧 Worker 后仍须审计文件，不在正常加载中推断引擎。
func MigrateCodexJournalRuntime(workerRoot string) error {
	marker := filepath.Join(workerRoot, "control-state", "codex-runtime-scope-v1")
	if data, err := os.ReadFile(marker); err == nil {
		if string(data) != "1\n" {
			return errors.New("无效的 Codex Journal 迁移标记")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Join(workerRoot, "control-state", "runs")
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	type change struct {
		path          string
		before, after []byte
	}
	var changes []change
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var document map[string]json.RawMessage
		var task, snapshot, runtime map[string]json.RawMessage
		if json.Unmarshal(data, &document) != nil ||
			json.Unmarshal(document["task"], &task) != nil ||
			json.Unmarshal(task["snapshot"], &snapshot) != nil ||
			json.Unmarshal(snapshot["runtime"], &runtime) != nil || runtime == nil {
			return fmt.Errorf("旧 Codex Journal %s 结构无效", entry.Name())
		}
		modern, err := validateJournalFormat(data)
		if err != nil {
			return fmt.Errorf("发现 Codex Journal %s 格式版本无效: %w", entry.Name(), err)
		}
		if raw, exists := runtime["engine"]; exists {
			var engine runtimeidentity.Engine
			if json.Unmarshal(raw, &engine) != nil || engine != runtimeidentity.Codex {
				return fmt.Errorf("发现 Codex Journal %s 包含错误引擎", entry.Name())
			}
			continue
		}
		if modern {
			return fmt.Errorf("新版 Codex Journal %s 缺少引擎", entry.Name())
		}
		if err := validateLegacyCodexJournal(entry.Name(), data, document, runtime); err != nil {
			return err
		}
		before := data
		runtime["engine"] = json.RawMessage(`"codex"`)
		snapshot["runtime"], err = json.Marshal(runtime)
		if err != nil {
			return err
		}
		task["snapshot"], err = json.Marshal(snapshot)
		if err != nil {
			return err
		}
		document["task"], err = json.Marshal(task)
		if err != nil {
			return err
		}
		document["journalFormatVersion"] = json.RawMessage(fmt.Sprint(currentJournalFormatVersion))
		data, err = json.Marshal(document)
		if err != nil {
			return err
		}
		changes = append(changes, change{path: path, before: before, after: data})
	}
	// 先校验全部文件，避免损坏文件排在后面时已改写前面的 journal。
	for _, item := range changes {
		if err := backupLegacyCodexJournal(item.path, item.before); err != nil {
			return err
		}
		if err := writePrivateStateFile(item.path, item.after); err != nil {
			return err
		}
	}
	return writePrivateStateFile(marker, []byte("1\n"))
}

// 只接受旧单 Codex 目录中可识别的完整旧结构；显式新版来源不在此回填。
// 早期未标记的双引擎 journal 若同时丢失 engine，无法与旧格式绝对区分。
func validateLegacyCodexJournal(name string, data []byte, document, runtime map[string]json.RawMessage) error {
	invalid := func() error { return fmt.Errorf("旧 Codex Journal %s 结构无效", name) }
	if _, exists := document["controlReportStopped"]; exists {
		return invalid()
	}
	required := map[string]any{
		"profileName": new(string), "sandbox": new(string), "approvalPolicy": new(string),
		"networkEnabled": new(bool), "settingsRevision": new(int64),
	}
	for key, target := range required {
		raw, exists := runtime[key]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return invalid()
		}
	}
	var journal runJournal
	if json.Unmarshal(data, &journal) != nil || journal.Task.Claimed.RunID == uuid.Nil ||
		name != journal.Task.Claimed.RunID.String()+".json" || journal.NextSequence < 1 {
		return invalid()
	}
	var previous int64
	for _, event := range journal.PendingEvents {
		if event.Sequence <= previous || event.Sequence >= journal.NextSequence || event.Type == "" {
			return invalid()
		}
		previous = event.Sequence
	}
	return nil
}

func backupLegacyCodexJournal(path string, data []byte) error {
	backup := path + ".before-runtime-scope"
	previous, err := os.ReadFile(backup)
	if errors.Is(err, os.ErrNotExist) {
		return writePrivateStateFile(backup, data)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(previous, data) {
		return nil
	}
	// 回滚重写同一 run 时追加内容寻址备份，不覆盖第一次或任一次迁移历史。
	backup += fmt.Sprintf(".%x", sha256.Sum256(data))
	previous, err = os.ReadFile(backup)
	if errors.Is(err, os.ErrNotExist) {
		return writePrivateStateFile(backup, data)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(previous, data) {
		return fmt.Errorf("发现 Codex Journal 迁移备份校验失败: %s", filepath.Base(backup))
	}
	return nil
}
