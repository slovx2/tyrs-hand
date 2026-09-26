package worker

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexJournalRuntimeMigrationPreservesPendingWork(t *testing.T) {
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	runID := uuid.New()
	legacy := map[string]any{
		"task": map[string]any{"claimed": map[string]any{"RunID": runID},
			"snapshot": map[string]any{"runtime": map[string]any{"model": "legacy-model",
				"profileName": "", "sandbox": "", "approvalPolicy": "", "networkEnabled": false, "settingsRevision": 0}}},
		"nextSequence":  3,
		"pendingEvents": []map[string]any{{"sequence": 2, "type": "turn.started"}},
		"unknownField":  map[string]any{"keep": true},
	}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path(runID), raw, 0o600))
	_, err = store.loadAll()
	require.Error(t, err, "正常加载不能默认推断引擎")
	require.NoError(t, MigrateCodexJournalRuntime(root))
	require.NoError(t, MigrateCodexJournalRuntime(root))
	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "codex", string(loaded[0].Task.Snapshot.Runtime.Engine))
	require.Equal(t, runID, loaded[0].Task.Claimed.RunID)
	require.EqualValues(t, 3, loaded[0].NextSequence)
	require.Len(t, loaded[0].PendingEvents, 1)
	backup, err := os.ReadFile(store.path(runID) + ".before-runtime-scope")
	require.NoError(t, err)
	require.Equal(t, raw, backup)
	migrated, err := os.ReadFile(store.path(runID))
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(migrated, &document))
	require.Equal(t, legacy["unknownField"], document["unknownField"])
	// 旧 Worker 回滚后重写同一 run，再次迁移保留每一代原始备份。
	legacy["nextSequence"] = 4
	rawAgain, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path(runID), rawAgain, 0o600))
	require.NoError(t, MigrateCodexJournalRuntime(root))
	_, err = store.loadAll()
	require.NoError(t, err)
	backupAgain, err := os.ReadFile(store.path(runID) + fmt.Sprintf(".before-runtime-scope.%x", sha256.Sum256(rawAgain)))
	require.NoError(t, err)
	require.Equal(t, rawAgain, backupAgain)
	backup, err = os.ReadFile(store.path(runID) + ".before-runtime-scope")
	require.NoError(t, err)
	require.Equal(t, raw, backup)
	// 同一旧内容重复迁移不会覆盖历史或再追加备份。
	require.NoError(t, os.WriteFile(store.path(runID), raw, 0o600))
	require.NoError(t, MigrateCodexJournalRuntime(root))
	_, err = store.loadAll()
	require.NoError(t, err)
}

func TestCodexJournalMigrationRejectsWrongRuntime(t *testing.T) {
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	for _, engine := range []string{`"claude-code"`, `""`, `null`} {
		raw := []byte(`{"task":{"snapshot":{"runtime":{"engine":` + engine + `}}}}`)
		path := store.path(uuid.New())
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		require.Error(t, MigrateCodexJournalRuntime(root))
		_, err = os.Stat(filepath.Join(root, "control-state", "codex-runtime-scope-v1"))
		require.ErrorIs(t, err, os.ErrNotExist)
		unchanged, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, raw, unchanged)
		require.NoError(t, os.Remove(path))
	}
}

func TestCodexJournalMigrationAuditsAfterMarker(t *testing.T) {
	for _, changed := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"empty-runtime", func(value map[string]any) { value["runtime"] = map[string]any{} }},
		{"null-runtime", func(value map[string]any) { value["runtime"] = nil }},
		{"explicit-claude", func(value map[string]any) { value["runtime"].(map[string]any)["engine"] = "claude-code" }},
		{"explicit-null", func(value map[string]any) { value["runtime"].(map[string]any)["engine"] = nil }},
		{"explicit-empty", func(value map[string]any) { value["runtime"].(map[string]any)["engine"] = "" }},
		{"invalid-network", func(value map[string]any) { value["runtime"].(map[string]any)["networkEnabled"] = "false" }},
	} {
		t.Run(changed.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := newJournalStore(root)
			require.NoError(t, err)
			require.NoError(t, MigrateCodexJournalRuntime(root))
			id := uuid.New()
			snapshot := map[string]any{"runtime": map[string]any{"profileName": "", "sandbox": "",
				"approvalPolicy": "", "networkEnabled": false, "settingsRevision": 0}}
			changed.edit(snapshot)
			data, err := json.Marshal(map[string]any{"task": map[string]any{
				"claimed": map[string]any{"RunID": id}, "snapshot": snapshot}, "nextSequence": 1})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(store.path(id), data, 0o600))
			require.Error(t, MigrateCodexJournalRuntime(root))
			after, err := os.ReadFile(store.path(id))
			require.NoError(t, err)
			require.Equal(t, data, after)
			_, err = os.Stat(store.path(id) + ".before-runtime-scope")
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestCodexJournalMigrationRejectsNewWriterWithoutEngine(t *testing.T) {
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	journal := &runJournal{NextSequence: 1}
	journal.Task.Claimed.RunID = uuid.New()
	journal.Task.Snapshot.Runtime.Engine = "codex"
	require.NoError(t, store.save(journal))
	data, err := os.ReadFile(store.path(journal.Task.Claimed.RunID))
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))
	require.EqualValues(t, currentJournalFormatVersion, value["journalFormatVersion"])
	runtime := value["task"].(map[string]any)["snapshot"].(map[string]any)["runtime"].(map[string]any)
	delete(runtime, "engine")
	data, err = json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path(journal.Task.Claimed.RunID), data, 0o600))
	require.ErrorContains(t, MigrateCodexJournalRuntime(root), "新版 Codex Journal")
	after, err := os.ReadFile(store.path(journal.Task.Claimed.RunID))
	require.NoError(t, err)
	require.Equal(t, data, after)
}

func TestCodexJournalMigrationRejectsDamagedLegacy(t *testing.T) {
	for _, changed := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"new-version", func(value map[string]any) { value["journalFormatVersion"] = 1 }},
		{"unknown-version", func(value map[string]any) { value["journalFormatVersion"] = 2 }},
		{"null-version", func(value map[string]any) { value["journalFormatVersion"] = nil }},
		{"zero-sequence", func(value map[string]any) { value["nextSequence"] = 0 }},
		{"missing-run", func(value map[string]any) { value["task"].(map[string]any)["claimed"] = nil }},
		{"wrong-run", func(value map[string]any) {
			value["task"].(map[string]any)["claimed"].(map[string]any)["RunID"] = uuid.New().String()
		}},
		{"new-field", func(value map[string]any) { value["controlReportStopped"] = true }},
		{"bad-events", func(value map[string]any) {
			value["pendingEvents"] = []any{map[string]any{"sequence": 2, "type": "turn.started"}}
		}},
	} {
		t.Run(changed.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := newJournalStore(root)
			require.NoError(t, err)
			journal := &runJournal{NextSequence: 1}
			journal.Task.Claimed.RunID = uuid.New()
			journal.Task.Snapshot.Runtime.Engine = "codex"
			require.NoError(t, store.save(journal))
			path := store.path(journal.Task.Claimed.RunID)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var value map[string]any
			require.NoError(t, json.Unmarshal(data, &value))
			delete(value, "journalFormatVersion")
			delete(value["task"].(map[string]any)["snapshot"].(map[string]any)["runtime"].(map[string]any), "engine")
			changed.edit(value)
			data, err = json.Marshal(value)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, data, 0o600))
			require.Error(t, MigrateCodexJournalRuntime(root))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, data, after)
		})
	}
}

func TestJournalLoaderRejectsUnknownFormatForEachEngine(t *testing.T) {
	for _, engine := range []string{"codex", "claude-code"} {
		t.Run(engine, func(t *testing.T) {
			store, err := newJournalStore(t.TempDir())
			require.NoError(t, err)
			id := uuid.New()
			for _, version := range []string{`2`, `null`, `"1"`, `true`, `1.5`} {
				data := []byte(fmt.Sprintf(`{"journalFormatVersion":%s,"task":{"claimed":{"RunID":%q},"snapshot":{"runtime":{"engine":%q}}},"nextSequence":1}`, version, id, engine))
				require.NoError(t, os.WriteFile(store.path(id), data, 0o600))
				_, err = store.loadAll()
				require.ErrorContains(t, err, "不支持的 Journal 格式版本")
			}
		})
	}
}
