package settings

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestProviderSignatureAndURLValidation(t *testing.T) {
	record := providerRecord{AgentProvider: AgentProvider{
		ModelSource: ModelSourceProvider, BaseURL: "https://api.example.com/v1",
	}, CredentialVersion: "v1"}
	first := signature(record)
	require.Len(t, first, 64)
	require.Equal(t, first, signature(record))
	record.CredentialVersion = "v2"
	require.NotEqual(t, first, signature(record))
	require.NoError(t, validateURL("", "URL"))
	require.NoError(t, validateURL("https://example.com/path", "URL"))
	require.Error(t, validateURL("relative/path", "URL"))
}

func TestProviderConnectionChangedIgnoresRuntimeDefaults(t *testing.T) {
	old := providerRecord{AgentProvider: AgentProvider{
		ModelSource: ModelSourceProvider, BaseURL: "https://api.example.com/v1",
		ProxyURL: "https://proxy.example.com",
		Model:    "gpt-5.5", Reasoning: "medium", ServiceTier: "standard",
	}, CredentialVersion: "v1"}
	current := old
	current.Model = "gpt-5.6-sol"
	current.Reasoning = "xhigh"
	current.ServiceTier = "fast"
	require.False(t, providerConnectionChanged(old, current))

	current.CredentialVersion = "v2"
	require.True(t, providerConnectionChanged(old, current))
}

func TestWriteSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, writeSecretFile(path, []byte("secret")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSyncChatGPTAuthOnlyOverwritesOnRevisionChanges(t *testing.T) {
	sharedHome := t.TempDir()
	codexHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sharedHome, "auth.json"),
		[]byte(`{"tokens":{"access_token":"shared"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "auth.json"),
		[]byte(`{"tokens":{"access_token":"refreshed"}}`), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(codexHome, ".tyrs-hand-chatgpt-auth-revision"),
		[]byte("2"), 0o600))

	require.NoError(t, SyncChatGPTAuth(codexHome, sharedHome, true, 2))
	data, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"tokens":{"access_token":"refreshed"}}`, string(data))

	require.NoError(t, SyncChatGPTAuth(codexHome, sharedHome, true, 3))
	data, err = os.ReadFile(filepath.Join(codexHome, "auth.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"tokens":{"access_token":"shared"}}`, string(data))

	require.NoError(t, SyncChatGPTAuth(codexHome, sharedHome, false, 4))
	_, err = os.Stat(filepath.Join(codexHome, "auth.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWriteGlobalAgents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	require.NoError(t, WriteGlobalAgents(path, "# Shared\n"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# Shared\n", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	require.NoError(t, WriteGlobalAgents(path, ""))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestSaveGlobalAgentsNormalizesAndPersistsContent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db, nil)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO platform_settings").
		WithArgs(globalAgentsKey, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, service.SaveGlobalAgents(context.Background(), GlobalAgents{Content: "# Shared\r\n"}))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO platform_settings").
		WithArgs(globalAgentsKey, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	require.NoError(t, service.SaveGlobalAgents(context.Background(), GlobalAgents{Content: "# Shared\n"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveGlobalAgentsReturnsDatabaseFailures(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(sqlmock.Sqlmock)
		expected error
	}{
		{
			name: "无法开始事务",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
			},
			expected: sql.ErrConnDone,
		},
		{
			name: "无法保存设置",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec("INSERT INTO platform_settings").
					WithArgs(globalAgentsKey, sqlmock.AnyArg()).WillReturnError(sql.ErrConnDone)
				mock.ExpectRollback()
			},
			expected: sql.ErrConnDone,
		},
		{
			name: "无法提交事务",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec("INSERT INTO platform_settings").WithArgs(globalAgentsKey, sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit().WillReturnError(sql.ErrTxDone)
			},
			expected: sql.ErrTxDone,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			test.setup(mock)
			err = NewService(db, nil).SaveGlobalAgents(context.Background(), GlobalAgents{Content: "# Shared\n"})
			require.ErrorIs(t, err, test.expected)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGlobalAgentsParticipatesInProviderSignature(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db, nil)
	provider := []byte(`{"modelSource":"provider","providerConfigured":true,"configSignature":"provider-signature"}`)
	expectProvider := func(content string) {
		mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(agentProviderKey).
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(provider))
		mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(globalAgentsKey).
			WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow([]byte(`{"content":"` + content + `"}`)))
	}
	expectProvider("first")
	first, err := service.AgentProvider(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, "provider-signature", first.ConfigSignature)
	expectProvider("first")
	same, err := service.AgentProvider(context.Background())
	require.NoError(t, err)
	require.Equal(t, first.ConfigSignature, same.ConfigSignature)
	expectProvider("second")
	second, err := service.AgentProvider(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, first.ConfigSignature, second.ConfigSignature)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGlobalAgentsMissingCorruptAndSizeBoundaries(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db, nil)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(agentProviderKey).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(globalAgentsKey).
		WillReturnError(sql.ErrNoRows)
	provider, err := service.AgentProvider(context.Background())
	require.NoError(t, err)
	require.Equal(t, ModelSourceProvider, provider.ModelSource)
	require.False(t, provider.ProviderConfigured)
	require.False(t, provider.ChatGPTConfigured)
	require.Empty(t, provider.ConfigSignature)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(globalAgentsKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow([]byte(`{"content":`)))
	_, err = service.GlobalAgents(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, service.SaveGlobalAgents(context.Background(),
		GlobalAgents{Content: string(make([]byte, maxGlobalAgents+1))}), "256 KiB")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGlobalAgentsReturnsQueryFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := NewService(db, nil)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(agentProviderKey).
		WillReturnError(sql.ErrConnDone)
	_, err = service.AgentProvider(context.Background())
	require.ErrorIs(t, err, sql.ErrConnDone)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(agentProviderKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow([]byte(`{"modelSource":"chatgpt"}`)))
	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(globalAgentsKey).
		WillReturnError(sql.ErrConnDone)
	_, err = service.AgentProvider(context.Background())
	require.ErrorIs(t, err, sql.ErrConnDone)

	mock.ExpectQuery("SELECT value FROM platform_settings").WithArgs(globalAgentsKey).
		WillReturnError(sql.ErrConnDone)
	_, err = service.GlobalAgents(context.Background())
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAtomicSettingsWritersReturnFilesystemFailures(t *testing.T) {
	missingParent := filepath.Join(t.TempDir(), "missing", "auth.json")
	require.Error(t, writeSecretFile(missingParent, []byte("secret")))
	require.Error(t, WriteGlobalAgents(filepath.Join(t.TempDir(), "missing", "AGENTS.md"), "content"))

	directoryTarget := t.TempDir()
	require.Error(t, writeSecretFile(directoryTarget, []byte("secret")))
	require.Error(t, WriteGlobalAgents(directoryTarget, "content"))

}
