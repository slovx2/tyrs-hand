package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/stretchr/testify/require"
)

func TestNormalizeConversationTitle(t *testing.T) {
	got := normalizeConversationTitle("  修复\n\t登录流程\x00  ")
	require.Equal(t, "修复 登录流程", got)
	long := normalizeConversationTitle(strings.Repeat("界", 120))
	require.Equal(t, 100, utf8.RuneCountInString(long))
	require.Equal(t, "Codex 开发任务", fallbackTitle("\n\t"))
}

func TestFallbackTitleUsesSixtyUnicodeCharacters(t *testing.T) {
	got := fallbackTitle(strings.Repeat("任", 80))
	require.Equal(t, 60, utf8.RuneCountInString(got))
	require.True(t, strings.HasSuffix(got, "…"))
	require.Equal(t, "short title", fallbackTitle(" short   title "))
}

func TestTitleGeneratorSendsExactLunaRequest(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/responses", request.URL.Path)
		require.Equal(t, "Bearer test-api-key", request.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestBody))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"  修复\n登录标题  "}]}]}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, "api-key", true)
	defer closeDB()
	generator.client = server.Client()
	title, err := generator.generate(context.Background(), "第一句用户消息")
	require.NoError(t, err)
	require.Equal(t, "修复 登录标题", title)
	require.Equal(t, titleModel, requestBody["model"])
	require.Equal(t, "priority", requestBody["service_tier"])
	require.Equal(t, false, requestBody["store"])
	require.Equal(t, float64(64), requestBody["max_output_tokens"])
	require.Equal(t, "none", requestBody["reasoning"].(map[string]any)["effort"])
	input := requestBody["input"].([]any)[0].(map[string]any)
	require.Equal(t, "user", input["role"])
	require.Contains(t, input["content"], "第一句用户消息")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorFallsBackWhenProviderIsNotAPIKey(t *testing.T) {
	generator, mock, closeDB := titleGeneratorForProvider(t, "", "device-code", false)
	defer closeDB()
	_, err := generator.generate(context.Background(), "message")
	require.ErrorContains(t, err, "API Key")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRejectsBadOrEmptyResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "non-2xx", status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		{name: "missing-output", status: http.StatusOK, body: `{"output":[]}`},
		{name: "empty-title", status: http.StatusOK, body: `{"output":[{"content":[{"type":"output_text","text":"  "}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, "api-key", true)
			defer closeDB()
			generator.client = server.Client()
			_, err := generator.generate(context.Background(), "message")
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTitleGeneratorHonorsHTTPClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = response.Write([]byte(`{"output":[]}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, "api-key", true)
	defer closeDB()
	generator.client = &http.Client{Timeout: 5 * time.Millisecond}
	_, err := generator.generate(context.Background(), "message")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorConfiguresProviderProxy(t *testing.T) {
	generator := &TitleGenerator{}
	client, err := generator.httpClient("http://127.0.0.1:8888")
	require.NoError(t, err)
	transport := client.Transport.(*http.Transport)
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	proxy, err := transport.Proxy(request)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8888", proxy.String())
}

func TestTitleGeneratorRunOnceClaimsAndSchedulesFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	box, err := security.NewSecretBox([]byte(strings.Repeat("b", 32)))
	require.NoError(t, err)
	conversationID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, '')")).
		WithArgs("pending").WillReturnRows(sqlmock.NewRows([]string{"id", "thread_id", "body"}).
		AddRow(conversationID, "thread-1", strings.Repeat("消息", 40)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	providerJSON := []byte(`{"providerType":"device-code","configured":true,"configSignature":"test"}`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key = $1")).
		WithArgs("agent.provider").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(providerJSON))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key=$1")).
		WithArgs("codex.global_agents").WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID, fallbackTitle(strings.Repeat("消息", 40))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
		WithArgs("conversation-title:"+conversationID.String(), "thread.rename", "channels/thread-1",
			sqlmock.AnyArg(), "").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	generator := NewTitleGenerator(db, settings.NewService(db, secrets.NewStore(db, box)))
	worked, err := generator.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, worked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorReturnsIdleWithoutPendingConversation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, '')")).
		WithArgs("pending").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	generator := &TitleGenerator{db: db}
	worked, err := generator.RunOnce(context.Background())
	require.NoError(t, err)
	require.False(t, worked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRecoversGeneratingWithoutCallingProvider(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	conversationID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, '')")).
		WithArgs("generating").WillReturnRows(sqlmock.NewRows([]string{"id", "thread_id", "body"}).
		AddRow(conversationID, "thread-1", "中断前的首条消息"))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID, "中断前的首条消息").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
		WithArgs("conversation-title:"+conversationID.String(), "thread.rename", "channels/thread-1",
			sqlmock.AnyArg(), "").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, '')")).
		WithArgs("generating").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	generator := &TitleGenerator{db: db}
	require.NoError(t, generator.RecoverInterrupted(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func titleGeneratorForProvider(t *testing.T, baseURL, providerType string,
	configured bool,
) (*TitleGenerator, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	box, err := security.NewSecretBox([]byte(strings.Repeat("a", 32)))
	require.NoError(t, err)
	store := secrets.NewStore(db, box)
	providerJSON, err := json.Marshal(map[string]any{"providerType": providerType,
		"baseUrl": baseURL, "configured": configured, "configSignature": "test"})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key = $1")).
		WithArgs("agent.provider").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(providerJSON))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key=$1")).
		WithArgs("codex.global_agents").WillReturnError(sql.ErrNoRows)
	if providerType == "api-key" && configured {
		nonce, ciphertext, err := box.Encrypt([]byte("test-api-key"), "agent.provider.api_key")
		require.NoError(t, err)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT nonce, ciphertext FROM encrypted_secrets WHERE secret_key = $1")).
			WithArgs("agent.provider.api_key").WillReturnRows(sqlmock.NewRows([]string{"nonce", "ciphertext"}).
			AddRow(nonce, ciphertext))
	}
	return &TitleGenerator{db: db, settings: settings.NewService(db, store)}, mock, func() {
		_ = db.Close()
	}
}
