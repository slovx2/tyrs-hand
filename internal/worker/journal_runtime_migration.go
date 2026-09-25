package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// MigrateCodexJournalRuntime 只用于升级前的 Codex 状态目录，在持有 Worker 数据锁时调用。
// 迁移标记完成后，正常读写严格要求引擎，不在恢复流程中推断缺失值。
func MigrateCodexJournalRuntime(workerRoot string) error {
	marker := filepath.Join(workerRoot, "control-state", "codex-runtime-scope-v1")
	if data, err := os.ReadFile(marker); err == nil {
		if string(data) != "1\n" {
			return errors.New("无效的 Codex Journal 迁移标记")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Join(workerRoot, "control-state", "runs")
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
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
		if raw, exists := runtime["engine"]; exists {
			var engine runtimeidentity.Engine
			if json.Unmarshal(raw, &engine) != nil || engine != runtimeidentity.Codex {
				return fmt.Errorf("发现 Codex Journal %s 包含错误引擎", entry.Name())
			}
			continue
		}
		// 备份留在同目录，扩展名不参与 Journal 加载；原始事件和未知字段原样保留。
		backup := path + ".before-runtime-scope"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err = writePrivateStateFile(backup, data); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
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
		data, err = json.Marshal(document)
		if err != nil {
			return err
		}
		if err = writePrivateStateFile(path, data); err != nil {
			return err
		}
	}
	return writePrivateStateFile(marker, []byte("1\n"))
}
