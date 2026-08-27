package hostworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestScanProjectsIncludesWorkspaceRootAndRegisteredProjects(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "notes")
	require.NoError(t, os.Mkdir(child, 0o755))
	codexHome := t.TempDir()
	external := filepath.Join(t.TempDir(), "external")
	require.NoError(t, os.Mkdir(external, 0o755))
	externalCanonical, err := filepath.EvalSymlinks(external)
	require.NoError(t, err)
	state := map[string]any{
		"active-workspace-roots":         []string{external},
		"electron-saved-workspace-roots": []string{external},
		"electron-workspace-root-labels": map[string]string{filepath.Join(root, "notes"): "Notes"},
		"thread-workspace-root-hints":    map[string]string{"thread": external},
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"), data, 0o600))
	projects, err := ScanProjects(context.Background(), root, codexHome)
	require.NoError(t, err)
	require.Len(t, projects, 3)
	require.Equal(t, "workspace_root", projects[0].ProjectSource)
	require.Equal(t, "Workspace", projects[0].Name)
	var externalProjectFound bool
	for _, project := range projects {
		if project.ProjectSource == "codex_registered" {
			externalProjectFound = true
			require.True(t, project.Available)
			require.Equal(t, externalCanonical, project.HostPath)
		}
	}
	require.True(t, externalProjectFound)
}

func TestScanProjectsReadsCodexSQLiteThreads(t *testing.T) {
	root, codexHome := t.TempDir(), t.TempDir()
	registered := filepath.Join(t.TempDir(), "registered")
	require.NoError(t, os.Mkdir(registered, 0o755))
	db, err := sql.Open("sqlite", filepath.Join(codexHome, "state.sqlite"))
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE threads (cwd TEXT); INSERT INTO threads(cwd) VALUES(?),(?);", registered, registered)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	projects, err := ScanProjects(context.Background(), root, codexHome)
	require.NoError(t, err)
	count := 0
	for _, project := range projects {
		if project.ProjectSource == "codex_registered" {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestScanProjectsKeepsMissingRegisteredProject(t *testing.T) {
	root, codexHome := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	data, err := json.Marshal(map[string]any{"active-workspace-roots": []string{missing}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"), data, 0o600))
	projects, err := ScanProjects(context.Background(), root, codexHome)
	require.NoError(t, err)
	var found bool
	for _, project := range projects {
		if project.ProjectSource == "codex_registered" {
			found = true
			require.False(t, project.Available)
			require.NotEmpty(t, project.ScanError)
		}
	}
	require.True(t, found)
}
