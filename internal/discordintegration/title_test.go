package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNormalizeConversationTitle(t *testing.T) {
	got := normalizeConversationTitle("  修复\n\t登录流程\x00  ")
	require.Equal(t, "修复 登录流程", got)
	long := normalizeConversationTitle(strings.Repeat("界", 120))
	require.Equal(t, 50, utf8.RuneCountInString(long))
	require.True(t, strings.HasSuffix(long, "…"))
	require.Equal(t, "Codex 开发任务", fallbackTitle("\n\t"))
}

func TestFallbackTitleUsesFiftyUnicodeCharacters(t *testing.T) {
	got := fallbackTitle(strings.Repeat("任", 80))
	require.Equal(t, 50, utf8.RuneCountInString(got))
	require.True(t, strings.HasSuffix(got, "…"))
	require.Equal(t, "short title", fallbackTitle(" short   title "))
}

func TestSanitizeLogValueRemovesControlsAndLimitsLength(t *testing.T) {
	got := sanitizeLogValue(strings.Repeat("错", 520)+"\nsecret", 512)
	require.Equal(t, 512, utf8.RuneCountInString(got))
	require.NotContains(t, got, "\n")
	require.NotContains(t, got, "secret")
}

func TestCleanGeneratedTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "think-prefix-and-lines", input: "<think>分析\n过程</think>\n标题：\"修复 HTTP 400 与 Luna\"\n说明", want: "修复 HTTP 400 与 Luna"},
		{name: "english-prefix", input: "\nTitle: `Keep OpenCode file.go #42`\nextra", want: "Keep OpenCode file.go #42"},
		{name: "unclosed-think", input: "可用标题<think>不会结束", want: "可用标题"},
		{name: "only-think", input: "<think>只有分析</think>", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, cleanGeneratedTitle(test.input))
		})
	}
	long := cleanGeneratedTitle(strings.Repeat("技", 60))
	require.Equal(t, 50, utf8.RuneCountInString(long))
	require.True(t, strings.HasSuffix(long, "…"))
}

func TestTitleGeneratorSendsExactLunaRequest(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/responses", request.URL.Path)
		require.Equal(t, "Bearer test-api-key", request.Header.Get("Authorization"))
		require.Equal(t, "application/json", request.Header.Get("Accept"))
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestBody))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"标题：修复 HTTP 400 与登录标题\n其他内容"}]}]}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
	defer closeDB()
	generator.client = server.Client()
	claimed := claimedConversationTitle{ID: uuid.New(), ThreadID: "thread-1", Body: "第一句用户消息中的 file.go 和 HTTP 400"}
	title, err := generator.generate(context.Background(), claimed)
	require.NoError(t, err)
	require.Equal(t, "修复 HTTP 400 与登录标题", title)
	require.Equal(t, titleModel, requestBody["model"])
	require.Equal(t, "priority", requestBody["service_tier"])
	require.Equal(t, false, requestBody["store"])
	require.Equal(t, false, requestBody["stream"])
	require.NotContains(t, requestBody, "max_output_tokens")
	require.Equal(t, "low", requestBody["reasoning"].(map[string]any)["effort"])
	input := requestBody["input"].([]any)
	developerInput := input[0].(map[string]any)
	require.Equal(t, "developer", developerInput["role"])
	require.Contains(t, developerInput["content"], "相同的语言")
	require.Contains(t, developerInput["content"], "HTTP 状态码")
	require.Contains(t, developerInput["content"], "50 个 Unicode 字符")
	userInput := input[1].(map[string]any)
	require.Equal(t, "user", userInput["role"])
	require.Equal(t, claimed.Body, userInput["content"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorFallsBackWithoutProviderConfiguration(t *testing.T) {
	generator, mock, closeDB := titleGeneratorForProvider(t, "", false)
	defer closeDB()
	_, err := generator.generate(context.Background(), claimedConversationTitle{Body: "message"})
	require.ErrorContains(t, err, "API Key")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRejectsBadOrEmptyResponsesWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		kind string
	}{
		{name: "malformed-json", body: `{`, kind: "response_parse_error"},
		{name: "missing-output", body: `{"output":[]}`, kind: "empty_output"},
		{name: "empty-title", body: `{"output":[{"content":[{"type":"output_text","text":"  "}]}]}`, kind: "empty_output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				requestCount++
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
			defer closeDB()
			generator.client = server.Client()
			_, err := generator.generate(context.Background(), claimedConversationTitle{Body: "message"})
			require.Error(t, err)
			var generationErr *titleGenerationError
			require.ErrorAs(t, err, &generationErr)
			require.Equal(t, test.kind, generationErr.kind)
			require.Equal(t, 1, requestCount)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTitleGeneratorRejectsSSEProtocolWithoutRetry(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount++
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
	defer closeDB()
	generator.client = server.Client()
	_, err := generator.generate(context.Background(), claimedConversationTitle{Body: "message"})
	require.Error(t, err)
	var generationErr *titleGenerationError
	require.ErrorAs(t, err, &generationErr)
	require.Equal(t, "protocol_error", generationErr.kind)
	require.Equal(t, 1, requestCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorDoesNotRetryHTTP400AndSanitizesLogs(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount++
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"error":{"message":"bad\nrequest","type":"invalid_request_error","code":"bad_field"}}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
	defer closeDB()
	generator.client = server.Client()
	core, logs := observer.New(zapcore.WarnLevel)
	generator.logger = zap.New(core)
	claimed := claimedConversationTitle{ID: uuid.New(), ThreadID: "thread-sensitive", Body: "private-user-message"}

	_, err := generator.generate(context.Background(), claimed)
	require.Error(t, err)
	require.Equal(t, 1, requestCount)
	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	require.Equal(t, "Luna 标题生成失败，使用回退标题", entry.Message)
	fields := entry.ContextMap()
	require.Equal(t, claimed.ID.String(), fields["conversation_id"])
	require.Equal(t, claimed.ThreadID, fields["thread_id"])
	require.Equal(t, "http_error", fields["error_kind"])
	require.EqualValues(t, http.StatusBadRequest, fields["http_status"])
	require.Equal(t, "invalid_request_error", fields["provider_error_type"])
	require.Equal(t, "bad_field", fields["provider_error_code"])
	require.Equal(t, "bad request", fields["provider_error_message"])
	require.Equal(t, true, fields["fallback"])
	encodedLogs, marshalErr := json.Marshal(logs.All())
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encodedLogs), claimed.Body)
	require.NotContains(t, string(encodedLogs), "test-api-key")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRetries429ThenSucceeds(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount++
		response.Header().Set("Content-Type", "application/json")
		if requestCount == 1 {
			response.WriteHeader(http.StatusTooManyRequests)
			_, _ = response.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit"}}`))
			return
		}
		_, _ = response.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"重试成功"}]}]}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
	defer closeDB()
	generator.client = server.Client()
	generator.retryDelay = time.Millisecond
	core, logs := observer.New(zapcore.WarnLevel)
	generator.logger = zap.New(core)

	title, err := generator.generate(context.Background(), claimedConversationTitle{ID: uuid.New(), ThreadID: "thread-429", Body: "message"})
	require.NoError(t, err)
	require.Equal(t, "重试成功", title)
	require.Equal(t, 2, requestCount)
	require.Equal(t, 1, logs.Len())
	require.Equal(t, "Luna 标题请求失败，准备重试", logs.All()[0].Message)
	require.Equal(t, false, logs.All()[0].ContextMap()["fallback"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRetriesServerErrorOnceThenFallsBack(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount++
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadGateway)
		_, _ = response.Write([]byte(`{"error":{"message":"upstream failed","type":"upstream_error"}}`))
	}))
	t.Cleanup(server.Close)
	generator, mock, closeDB := titleGeneratorForProvider(t, server.URL, true)
	defer closeDB()
	generator.client = server.Client()
	generator.retryDelay = time.Millisecond
	core, logs := observer.New(zapcore.WarnLevel)
	generator.logger = zap.New(core)

	_, err := generator.generate(context.Background(), claimedConversationTitle{ID: uuid.New(), ThreadID: "thread-502", Body: "message"})
	require.Error(t, err)
	require.Equal(t, 2, requestCount)
	require.Equal(t, 2, logs.Len())
	require.Equal(t, "Luna 标题请求失败，准备重试", logs.All()[0].Message)
	require.Equal(t, "Luna 标题生成失败，使用回退标题", logs.All()[1].Message)
	require.EqualValues(t, 2, logs.All()[1].ContextMap()["attempt"])
	require.Equal(t, true, logs.All()[1].ContextMap()["fallback"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRetriesNetworkErrorOnce(t *testing.T) {
	requestCount := 0
	generator, mock, closeDB := titleGeneratorForProvider(t, "https://provider.example/v1", true)
	defer closeDB()
	generator.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("temporary network failure")
	})}
	generator.retryDelay = time.Millisecond

	_, err := generator.generate(context.Background(), claimedConversationTitle{Body: "message"})
	require.Error(t, err)
	require.Equal(t, 2, requestCount)
	var generationErr *titleGenerationError
	require.ErrorAs(t, err, &generationErr)
	require.Equal(t, "network_error", generationErr.kind)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorAppliesTotalTimeout(t *testing.T) {
	requestCount := 0
	generator, mock, closeDB := titleGeneratorForProvider(t, "https://provider.example/v1", true)
	defer closeDB()
	generator.timeout = 15 * time.Millisecond
	generator.retryDelay = time.Millisecond
	generator.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	started := time.Now()

	_, err := generator.generate(context.Background(), claimedConversationTitle{Body: "message"})
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, 1, requestCount)
	var generationErr *titleGenerationError
	require.ErrorAs(t, err, &generationErr)
	require.Equal(t, "timeout_error", generationErr.kind)
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, desktop.first_input_text, '')")).
		WithArgs("pending").WillReturnRows(sqlmock.NewRows([]string{"id", "thread_id", "body"}).
		AddRow(conversationID, "thread-1", strings.Repeat("消息", 40)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	providerJSON := []byte(`{"modelSource":"chatgpt","chatgptConfigured":true,"configSignature":"test"}`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key = $1")).
		WithArgs("agent.provider").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(providerJSON))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key=$1")).
		WithArgs("codex.global_agents").WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID, fallbackTitle(strings.Repeat("消息", 40))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls SET")).
		WithArgs(conversationID, fallbackTitle(strings.Repeat("消息", 40))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
		WithArgs("conversation-title:"+conversationID.String(), "thread.rename", "channels/thread-1",
			sqlmock.AnyArg(), "").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	generator := NewTitleGenerator(db, settings.NewService(db, secrets.NewStore(db, box)), zap.NewNop())
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, desktop.first_input_text, '')")).
		WithArgs("pending").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	generator := &TitleGenerator{db: db}
	worked, err := generator.RunOnce(context.Background())
	require.NoError(t, err)
	require.False(t, worked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorClaimsDesktopFirstInput(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	conversationID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, desktop.first_input_text, '')")).
		WithArgs("pending").WillReturnRows(sqlmock.NewRows([]string{"id", "thread_id", "body"}).
		AddRow(conversationID, "desktop-discord-thread", "Desktop 首条输入"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claimed, err := (&TitleGenerator{db: db}).claim(context.Background(), "pending")
	require.NoError(t, err)
	require.Equal(t, conversationID, claimed.ID)
	require.Equal(t, "desktop-discord-thread", claimed.ThreadID)
	require.Equal(t, "Desktop 首条输入", claimed.Body)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTitleGeneratorRecoversGeneratingWithoutCallingProvider(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	conversationID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, desktop.first_input_text, '')")).
		WithArgs("generating").WillReturnRows(sqlmock.NewRows([]string{"id", "thread_id", "body"}).
		AddRow(conversationID, "thread-1", "中断前的首条消息"))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations")).
		WithArgs(conversationID, "中断前的首条消息").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls SET")).
		WithArgs(conversationID, "中断前的首条消息").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
		WithArgs("conversation-title:"+conversationID.String(), "thread.rename", "channels/thread-1",
			sqlmock.AnyArg(), "").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id, c.thread_id, COALESCE(m.body, desktop.first_input_text, '')")).
		WithArgs("generating").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	generator := &TitleGenerator{db: db}
	require.NoError(t, generator.RecoverInterrupted(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func titleGeneratorForProvider(t *testing.T, baseURL string, configured bool,
) (*TitleGenerator, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	box, err := security.NewSecretBox([]byte(strings.Repeat("a", 32)))
	require.NoError(t, err)
	store := secrets.NewStore(db, box)
	providerJSON, err := json.Marshal(map[string]any{"modelSource": "provider",
		"baseUrl": baseURL, "providerConfigured": configured, "configSignature": "test"})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key = $1")).
		WithArgs("agent.provider").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(providerJSON))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM platform_settings WHERE setting_key=$1")).
		WithArgs("codex.global_agents").WillReturnError(sql.ErrNoRows)
	if configured {
		nonce, ciphertext, err := box.Encrypt([]byte("test-api-key"), "agent.provider.api_key")
		require.NoError(t, err)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT nonce, ciphertext FROM encrypted_secrets WHERE secret_key = $1")).
			WithArgs("agent.provider.api_key").WillReturnRows(sqlmock.NewRows([]string{"nonce", "ciphertext"}).
			AddRow(nonce, ciphertext))
	}
	return &TitleGenerator{db: db, settings: settings.NewService(db, store), logger: zap.NewNop()}, mock, func() {
		_ = db.Close()
	}
}
