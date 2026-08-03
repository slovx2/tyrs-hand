package settings

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestInstallBuiltinSkills(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, InstallBuiltinSkills(home))
	skillPath := filepath.Join(home, "skills", "tyrs-browser", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "name: tyrs-browser")
	require.FileExists(t, filepath.Join(home, "skills", "tyrs-browser", "agents", "openai.yaml"))
	require.Len(t, BuiltinSkillsRevision(), 64)
	require.Equal(t, BuiltinSkillsRevision(), BuiltinSkillsRevision())

	require.NoError(t, os.WriteFile(skillPath, []byte("stale"), 0o600))
	require.NoError(t, InstallBuiltinSkills(home))
	data, err = os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "name: tyrs-browser")
}

func TestGitHubAgentInstructionsReadBoundaries(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(githubAgentInstructionsKey).
		WillReturnError(sql.ErrNoRows)
	empty, err := service.GitHubAgentInstructions(context.Background())
	require.NoError(t, err)
	require.Empty(t, empty.Content)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(githubAgentInstructionsKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow([]byte(`{"content":"GitHub only"}`)))
	value, err := service.GitHubAgentInstructions(context.Background())
	require.NoError(t, err)
	require.Equal(t, "GitHub only", value.Content)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(githubAgentInstructionsKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow([]byte(`{"content":`)))
	_, err = service.GitHubAgentInstructions(context.Background())
	require.Error(t, err)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(githubAgentInstructionsKey).
		WillReturnError(sql.ErrConnDone)
	_, err = service.GitHubAgentInstructions(context.Background())
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveGitHubAgentInstructionsNormalizesAndPersists(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db)

	mock.ExpectExec("INSERT INTO platform_settings").
		WithArgs(githubAgentInstructionsKey, []byte(`{"content":"# Agent\n"}`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, service.SaveGitHubAgentInstructions(context.Background(),
		GitHubAgentInstructions{Content: "# Agent\r\n"}))

	require.ErrorContains(t, service.SaveGitHubAgentInstructions(context.Background(),
		GitHubAgentInstructions{Content: strings.Repeat("x", maxInstructions+1)}), "256 KiB")
	mock.ExpectExec("INSERT INTO platform_settings").WithArgs(githubAgentInstructionsKey, sqlmock.AnyArg()).
		WillReturnError(sql.ErrConnDone)
	require.ErrorIs(t, service.SaveGitHubAgentInstructions(context.Background(),
		GitHubAgentInstructions{Content: "content"}), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}
