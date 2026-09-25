package worker

import (
	"encoding/json"
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
			"snapshot": map[string]any{"runtime": map[string]any{"model": "legacy-model"}}},
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
	// 完成标记之后出现的无引擎文件是错误数据，不再次执行旧数据回填。
	require.NoError(t, os.WriteFile(store.path(runID), raw, 0o600))
	require.NoError(t, MigrateCodexJournalRuntime(root))
	_, err = store.loadAll()
	require.Error(t, err)
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
