package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
)

const expoPushEndpoint = "https://exp.host/--/api/v2/push/send"

type clientNotification struct {
	ID              uuid.UUID
	AdministratorID uuid.UUID
	Title           string
	Body            string
	Data            json.RawMessage
	AttemptCount    int
}

// RunBackground 投递移动端通知并清理协议保留期数据；生命周期与 HTTP Server 一致。
func (s *Server) RunBackground(ctx context.Context) error {
	titles := discordintegration.NewTitleGenerator(s.db, s.settings, s.logger)
	if err := titles.RecoverInterruptedSessions(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			worked, err := titles.RunSessionOnce(ctx)
			if err != nil && s.logger != nil {
				s.logger.Warn("生成 Session 标题失败")
			}
			if !worked {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}
	}()
	pushTicker := time.NewTicker(2 * time.Second)
	cleanupTicker := time.NewTicker(time.Hour)
	defer pushTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pushTicker.C:
			for range 20 {
				worked, err := s.dispatchClientNotification(ctx)
				if err != nil || !worked {
					break
				}
			}
		case <-cleanupTicker.C:
			s.cleanupClientProtocol(ctx)
		}
	}
}

func (s *Server) dispatchClientNotification(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var item clientNotification
	err = tx.QueryRowContext(ctx, `SELECT id,administrator_id,title,body,data,attempt_count
		FROM client_notification_outbox WHERE status IN ('pending','retrying')
		AND available_at<=now() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&item.ID, &item.AdministratorID, &item.Title, &item.Body, &item.Data,
			&item.AttemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE client_notification_outbox SET status='sending',
		attempt_count=attempt_count+1,updated_at=now() WHERE id=$1`, item.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token.expo_push_token FROM client_push_tokens token
		JOIN client_devices device ON device.id=token.device_id
		WHERE device.administrator_id=$1 AND token.enabled=true`, item.AdministratorID)
	if err != nil {
		return true, s.retryClientNotification(ctx, item, err)
	}
	var tokens []string
	for rows.Next() {
		var token string
		if err = rows.Scan(&token); err != nil {
			_ = rows.Close()
			return true, s.retryClientNotification(ctx, item, err)
		}
		tokens = append(tokens, token)
	}
	if err = rows.Close(); err != nil {
		return true, s.retryClientNotification(ctx, item, err)
	}
	if len(tokens) == 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE client_notification_outbox SET status='delivered',
			delivered_at=now(),updated_at=now() WHERE id=$1`, item.ID)
		return true, err
	}
	messages := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		messages = append(messages, map[string]any{"to": token, "title": item.Title,
			"body": item.Body, "data": json.RawMessage(item.Data), "sound": "default"})
	}
	encoded, _ := json.Marshal(messages)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, expoPushEndpoint,
		bytes.NewReader(encoded))
	if err != nil {
		return true, s.retryClientNotification(ctx, item, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return true, s.retryClientNotification(ctx, item, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return true, s.retryClientNotification(ctx, item,
			fmt.Errorf("Expo Push 返回 HTTP %d", response.StatusCode))
	}
	var result struct {
		Data []struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details struct {
				Error string `json:"error"`
			} `json:"details"`
		} `json:"data"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return true, s.retryClientNotification(ctx, item, err)
	}
	var transient error
	for index, receipt := range result.Data {
		if receipt.Status == "ok" {
			continue
		}
		if index < len(tokens) && receipt.Details.Error == "DeviceNotRegistered" {
			clientInvalidPushTokens.Inc()
			_, _ = s.db.ExecContext(ctx, `DELETE FROM client_push_tokens WHERE expo_push_token=$1`,
				tokens[index])
			continue
		}
		transient = errors.New(receipt.Message)
	}
	if transient != nil {
		return true, s.retryClientNotification(ctx, item, transient)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE client_notification_outbox SET status='delivered',
		delivered_at=now(),last_error=NULL,updated_at=now() WHERE id=$1`, item.ID)
	return true, err
}

func (s *Server) retryClientNotification(ctx context.Context, item clientNotification,
	cause error,
) error {
	status := "retrying"
	if item.AttemptCount+1 >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(item.AttemptCount, 6)) * time.Minute
	_, err := s.db.ExecContext(ctx, `UPDATE client_notification_outbox SET status=$2,
		available_at=now()+$3::interval,last_error=$4,updated_at=now() WHERE id=$1`,
		item.ID, status, fmt.Sprintf("%f seconds", delay.Seconds()), cause.Error())
	return err
}

func (s *Server) cleanupClientProtocol(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM client_updates WHERE created_at<$1`,
		clientSyncRetentionStart())
	rows, err := s.db.QueryContext(ctx, `DELETE FROM session_attachments WHERE status='uploaded'
		AND created_at<now()-interval '24 hours' RETURNING storage_key`)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var storageKey string
		if rows.Scan(&storageKey) == nil {
			if path, pathErr := s.clientAttachmentPath(storageKey); pathErr == nil {
				_ = os.Remove(path)
			}
		}
	}
}
