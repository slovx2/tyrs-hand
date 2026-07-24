package discordintegration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/settings"
)

const (
	titleModel   = "gpt-5.6-luna"
	titleTimeout = 15 * time.Second
)

type claimedConversationTitle struct {
	ID       uuid.UUID
	ThreadID string
	Body     string
}

// TitleGenerator 独立于 Codex Turn 生成 Discord 帖子标题。
type TitleGenerator struct {
	db       *sql.DB
	settings *settings.Service
	client   *http.Client
}

func NewTitleGenerator(db *sql.DB, settingsService *settings.Service) *TitleGenerator {
	return &TitleGenerator{db: db, settings: settingsService}
}

func (g *TitleGenerator) RunOnce(ctx context.Context) (bool, error) {
	claimed, err := g.claim(ctx, "pending")
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	title := fallbackTitle(claimed.Body)
	if generated, generateErr := g.generate(ctx, claimed.Body); generateErr == nil {
		title = generated
	}
	return true, g.schedule(ctx, claimed, title)
}

// RecoverInterrupted 将上次进程遗留的 generating 任务直接回退，不再次请求模型。
func (g *TitleGenerator) RecoverInterrupted(ctx context.Context) error {
	for {
		claimed, err := g.claim(ctx, "generating")
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := g.schedule(ctx, claimed, fallbackTitle(claimed.Body)); err != nil {
			return err
		}
	}
}

func (g *TitleGenerator) claim(ctx context.Context, status string) (claimedConversationTitle, error) {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return claimedConversationTitle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var claimed claimedConversationTitle
	err = tx.QueryRowContext(ctx, `SELECT c.id, c.thread_id, COALESCE(m.body, '')
		FROM discord_conversations c
		LEFT JOIN discord_input_messages m ON m.message_id = c.starter_message_id
		WHERE c.title_rename_status = $1 ORDER BY c.created_at, c.id
		FOR UPDATE OF c SKIP LOCKED LIMIT 1`, status).
		Scan(&claimed.ID, &claimed.ThreadID, &claimed.Body)
	if err != nil {
		return claimedConversationTitle{}, err
	}
	if status == "pending" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE discord_conversations
			SET title_rename_status = 'generating', updated_at = now()
			WHERE id = $1 AND title_rename_status = 'pending'`, claimed.ID)
		if updateErr != nil {
			return claimedConversationTitle{}, updateErr
		}
		if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
			if countErr != nil {
				return claimedConversationTitle{}, countErr
			}
			return claimedConversationTitle{}, errors.New("discord 标题认领状态已变化")
		}
	}
	return claimed, tx.Commit()
}

func (g *TitleGenerator) generate(ctx context.Context, body string) (string, error) {
	provider, err := g.settings.AgentProvider(ctx)
	if err != nil {
		return "", err
	}
	if !provider.ProviderConfigured {
		return "", errors.New("未配置 Agent Provider API Key")
	}
	apiKey, err := g.settings.APIKey(ctx)
	if err != nil || len(apiKey) == 0 {
		return "", errors.New("当前 Agent Provider API Key 不可用")
	}
	requestBody := map[string]any{
		"model": titleModel,
		"input": []map[string]any{{
			"role":    "user",
			"content": "请根据下面这条用户消息生成一个简洁、具体的中文 Discord 帖子标题。只输出标题，不要引号、标点说明或其他内容。\n\n" + body,
		}},
		"service_tier":      "priority",
		"reasoning":         map[string]string{"effort": "none"},
		"store":             false,
		"max_output_tokens": 64,
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	requestCtx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		baseURL+"/responses", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+string(apiKey))
	request.Header.Set("Content-Type", "application/json")
	client, err := g.httpClient(provider.ProxyURL)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("luna 标题请求返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	for _, output := range payload.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" {
				title := normalizeConversationTitle(content.Text)
				if title != "" {
					return title, nil
				}
			}
		}
	}
	return "", errors.New("luna 标题响应没有 output_text")
}

func (g *TitleGenerator) httpClient(proxyURL string) (*http.Client, error) {
	if g.client != nil {
		return g.client, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{Transport: transport, Timeout: titleTimeout}, nil
}

func (g *TitleGenerator) schedule(ctx context.Context, claimed claimedConversationTitle, title string) error {
	title = normalizeConversationTitle(title)
	if title == "" {
		title = fallbackTitle(claimed.Body)
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE discord_conversations
		SET title_rename_status = 'scheduled', generated_title = $2, updated_at = now()
		WHERE id = $1 AND title_rename_status = 'generating'`, claimed.ID, title)
	if err != nil {
		return err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		if countErr != nil {
			return countErr
		}
		return errors.New("discord 标题生成状态已变化")
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
		desired_thread_name = $2, desired_thread_name_source = 'luna',
		desired_thread_name_revision = desired_thread_name_revision + 1,
		thread_name_last_error = NULL, updated_at = now()
		WHERE discord_conversation_id = $1`,
		claimed.ID, title)
	if err != nil {
		return err
	}
	payload := map[string]any{"channelId": claimed.ThreadID, "threadName": title,
		"conversationId": claimed.ID.String()}
	if err := enqueueDiscordOutbox(ctx, tx, "conversation-title:"+claimed.ID.String(),
		"thread.rename", "channels/"+claimed.ThreadID, payload, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeConversationTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > 100 {
		value = string([]rune(value)[:100])
	}
	return strings.TrimSpace(value)
}

func fallbackTitle(body string) string {
	body = normalizeConversationTitle(body)
	if body == "" {
		return "Codex 开发任务"
	}
	if utf8.RuneCountInString(body) > 60 {
		return string([]rune(body)[:59]) + "…"
	}
	return body
}
