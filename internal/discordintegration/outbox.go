package discordintegration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
)

type OutboxItem struct {
	ID            string          `json:"id"`
	OperationKey  string          `json:"operationKey"`
	OperationType string          `json:"operationType"`
	RouteKey      string          `json:"routeKey"`
	Payload       json.RawMessage `json:"payload"`
	Nonce         string          `json:"nonce,omitempty"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"maxAttempts"`
	LeaseToken    string          `json:"-"`
}

type OutboxStore interface {
	Claim(context.Context, time.Duration) (*OutboxItem, error)
	Complete(context.Context, OutboxItem, json.RawMessage) error
	Retry(context.Context, OutboxItem, time.Time, error) error
	Fail(context.Context, OutboxItem, error) error
}

type SQLoutbox struct {
	db *sql.DB
}

func NewSQLoutbox(db *sql.DB) *SQLoutbox { return &SQLoutbox{db: db} }

func (s *SQLoutbox) Enqueue(ctx context.Context, operationKey, operationType, routeKey string, payload any, nonce string) error {
	return enqueueDiscordOutbox(ctx, s.db, operationKey, operationType, routeKey, payload, nonce)
}

type discordOutboxExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueDiscordOutbox(ctx context.Context, execer discordOutboxExecer,
	operationKey, operationType, routeKey string, payload any, nonce string,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	nonce = discordNonce(nonce)
	_, err = execer.ExecContext(ctx, `
		INSERT INTO integration_outbox(integration, operation_key, operation_type, route_key, payload, nonce)
		VALUES ('discord', $1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT(integration, operation_key) DO UPDATE SET
			operation_type = EXCLUDED.operation_type, route_key = EXCLUDED.route_key,
			payload = EXCLUDED.payload, nonce = EXCLUDED.nonce,
			status = CASE WHEN integration_outbox.status = 'sending' THEN 'sending' ELSE 'pending' END,
			attempt_count = CASE WHEN integration_outbox.status = 'sending' THEN integration_outbox.attempt_count ELSE 0 END,
			available_at = CASE
				WHEN integration_outbox.status = 'completed' THEN now() + interval '5 seconds'
				ELSE integration_outbox.available_at
			END,
			updated_at = now()`, operationKey, operationType, routeKey, encoded, nonce)
	return err
}

func discordNonce(value string) string {
	if len(value) <= 25 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "th-" + hex.EncodeToString(digest[:11])
}

func (s *SQLoutbox) Claim(ctx context.Context, lease time.Duration) (*OutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var item OutboxItem
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT id, operation_key, operation_type, route_key, payload, COALESCE(nonce, ''), attempt_count + 1, max_attempts
		FROM integration_outbox
		WHERE integration = 'discord' AND (
			(status IN ('pending', 'retrying') AND available_at <= now()
				AND (lease_expires_at IS NULL OR lease_expires_at < now()))
			OR (status = 'sending' AND lease_expires_at < now())
		)
		ORDER BY available_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&id, &item.OperationKey, &item.OperationType, &item.RouteKey, &item.Payload,
			&item.Nonce, &item.Attempt, &item.MaxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	item.ID, item.LeaseToken = id.String(), token
	_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET status = 'sending', attempt_count = $2,
		lease_token = $3, lease_expires_at = now() + $4::interval, updated_at = now() WHERE id = $1`,
		id, item.Attempt, token, intervalLiteral(lease))
	if err != nil {
		return nil, err
	}
	return &item, tx.Commit()
}

func (s *SQLoutbox) Complete(ctx context.Context, item OutboxItem, response json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var desktopRequestID uuid.UUID
	if strings.HasPrefix(item.OperationKey, "projection:") {
		var locked int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT 1 FROM discord_projections
			WHERE projection_key = $1 FOR UPDATE), 0)`,
			strings.TrimPrefix(item.OperationKey, "projection:")).Scan(&locked); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE integration_outbox SET
		status = CASE WHEN payload = $4::jsonb THEN 'completed' ELSE 'pending' END,
		available_at = CASE WHEN payload = $4::jsonb THEN available_at ELSE now() + interval '5 seconds' END,
		response = $3, lease_token = NULL, lease_expires_at = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND lease_token = $2`, item.ID, item.LeaseToken, nullableJSON(response), item.Payload)
	if err := changedOne(result, err); err != nil {
		return err
	}
	if strings.HasPrefix(item.OperationKey, "projection:") {
		var value struct {
			ThreadID  string `json:"threadId"`
			MessageID string `json:"messageId"`
		}
		_ = json.Unmarshal(response, &value)
		_, err = tx.ExecContext(ctx, `UPDATE discord_projections SET
			resource_id = COALESCE(NULLIF($3, ''), resource_id),
			message_id = COALESCE(NULLIF($2, ''), message_id),
			applied_version = CASE WHEN o.status = 'completed' THEN desired_version ELSE applied_version END,
			applied_at = CASE WHEN o.status = 'completed' THEN now() ELSE applied_at END,
			last_error = NULL, updated_at = now()
			FROM integration_outbox o WHERE projection_key = $1 AND o.id = $4`,
			strings.TrimPrefix(item.OperationKey, "projection:"), value.MessageID, value.ThreadID, item.ID)
		if err != nil {
			return err
		}
		if value.MessageID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE integration_outbox o SET
				operation_type = 'message.update', nonce = NULL,
				route_key = 'channels/' || p.resource_id || '/messages/' || $2,
				payload = o.payload || jsonb_build_object('channelId', p.resource_id, 'messageId', $2),
				updated_at = now()
				FROM discord_projections p
				WHERE o.id = $1 AND o.status = 'pending' AND p.projection_key = $3`,
				item.ID, value.MessageID, strings.TrimPrefix(item.OperationKey, "projection:"))
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "task-post:") {
		var sent struct {
			WorkItemID string `json:"workItemId"`
			ForumID    string `json:"forumId"`
			State      string `json:"state"`
		}
		var value struct {
			ThreadID  string `json:"threadId"`
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(item.Payload, &sent) != nil || json.Unmarshal(response, &value) != nil {
			return errors.New("任务 Post Outbox 结果无效")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO discord_task_posts
			(work_item_id, forum_id, thread_id, starter_message_id, last_state, last_projected_at)
			VALUES ($1, $2, $3, $4, $5, now()) ON CONFLICT(work_item_id) DO UPDATE SET
				thread_id = EXCLUDED.thread_id, starter_message_id = EXCLUDED.starter_message_id,
				last_state = EXCLUDED.last_state, last_projected_at = now()`,
			sent.WorkItemID, sent.ForumID, value.ThreadID, value.MessageID, sent.State)
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "desktop-thread-post:") {
		if err := s.completeDesktopThreadPost(ctx, tx, item, response); err != nil {
			return err
		}
		desktopRequestID, _ = uuid.Parse(strings.TrimPrefix(item.OperationKey, "desktop-thread-post:"))
	}
	if strings.HasPrefix(item.OperationKey, "interactive:") {
		var value struct {
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(response, &value) == nil && value.MessageID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE codex_interactive_requests SET
				discord_message_id=$2, updated_at=now() WHERE id=$1`,
				strings.TrimPrefix(item.OperationKey, "interactive:"), value.MessageID)
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "task-log:") || strings.HasPrefix(item.OperationKey, "task-card:") {
		var sent struct {
			WorkItemID string `json:"workItemId"`
			State      string `json:"state"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.WorkItemID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE discord_task_posts SET last_state = $2,
				last_projected_at = now() WHERE work_item_id = $1`, sent.WorkItemID, sent.State)
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "task-archive:") {
		var sent struct {
			WorkItemID string `json:"workItemId"`
			Archived   bool   `json:"archived"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.WorkItemID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE discord_task_posts SET archived = $2 WHERE work_item_id = $1`, sent.WorkItemID, sent.Archived)
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "conversation-title:") {
		conversationID := strings.TrimPrefix(item.OperationKey, "conversation-title:")
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET title_rename_status = 'completed',
			title = COALESCE(generated_title, title), title_renamed_at = now(), updated_at = now()
			WHERE id = $1 AND title_rename_status = 'scheduled'`, conversationID)
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "thread-name:") {
		var sent struct {
			ControlID string `json:"controlId"`
			Name      string `json:"threadName"`
			Revision  int64  `json:"revision"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.ControlID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
				applied_thread_name = $2, applied_thread_name_revision = $3,
				thread_name_last_error = NULL, updated_at = now()
				WHERE id = $1 AND desired_thread_name_revision = $3`,
				sent.ControlID, sent.Name, sent.Revision)
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "conversation-lifecycle-card:") {
		var sent struct {
			ConversationID string `json:"conversationId"`
			LifecycleState string `json:"lifecycleState"`
			Revision       int64  `json:"revision"`
		}
		var value struct {
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.ConversationID != "" {
			_ = json.Unmarshal(response, &value)
			var threadID string
			err = tx.QueryRowContext(ctx, `UPDATE discord_conversations SET
				lifecycle_card_message_id = COALESCE(NULLIF($4,''), lifecycle_card_message_id),
				lifecycle_projection_error = NULL, updated_at = now()
				WHERE id = $1 AND lifecycle_state = $2 AND lifecycle_revision = $3
				RETURNING thread_id`, sent.ConversationID, sent.LifecycleState,
				sent.Revision, value.MessageID).Scan(&threadID)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else if err == nil && sent.LifecycleState == "archived" {
				conversationID, parseErr := uuid.Parse(sent.ConversationID)
				if parseErr != nil {
					return parseErr
				}
				err = enqueueThreadLifecycle(ctx, tx, conversationID, threadID,
					sent.LifecycleState, sent.Revision)
			}
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "conversation-lifecycle:") {
		var sent struct {
			ConversationID string `json:"conversationId"`
			LifecycleState string `json:"lifecycleState"`
			Revision       int64  `json:"revision"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.ConversationID != "" {
			var threadID, cardMessageID string
			err = tx.QueryRowContext(ctx, `UPDATE discord_conversations SET
				discord_lifecycle_applied_revision = $3,
				lifecycle_projection_error = NULL, updated_at = now()
				WHERE id = $1 AND lifecycle_state = $2 AND lifecycle_revision = $3
				RETURNING thread_id, COALESCE(lifecycle_card_message_id,'')`,
				sent.ConversationID, sent.LifecycleState, sent.Revision).
				Scan(&threadID, &cardMessageID)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else if err == nil && sent.LifecycleState == "active" && cardMessageID != "" {
				err = enqueueDiscordOutbox(ctx, tx,
					"conversation-lifecycle-delete:"+sent.ConversationID+":"+strconv.FormatInt(sent.Revision, 10),
					"message.delete", "channels/"+threadID+"/messages/"+cardMessageID,
					map[string]any{"channelId": threadID, "messageId": cardMessageID,
						"conversationId": sent.ConversationID,
						"lifecycleState": sent.LifecycleState, "revision": sent.Revision}, "")
			}
			if err != nil {
				return err
			}
		}
	}
	if strings.HasPrefix(item.OperationKey, "conversation-lifecycle-delete:") {
		var sent struct {
			ConversationID string `json:"conversationId"`
			LifecycleState string `json:"lifecycleState"`
			Revision       int64  `json:"revision"`
			MessageID      string `json:"messageId"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.ConversationID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET
				lifecycle_card_message_id = NULL, lifecycle_projection_error = NULL,
				updated_at = now() WHERE id = $1 AND lifecycle_state = 'active'
				AND lifecycle_revision = $2 AND lifecycle_card_message_id = $3`,
				sent.ConversationID, sent.Revision, sent.MessageID)
			if err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if desktopRequestID != uuid.Nil {
		return s.replayDesktopProjection(ctx, desktopRequestID)
	}
	return nil
}

func (s *SQLoutbox) Retry(ctx context.Context, item OutboxItem, at time.Time, cause error) error {
	result, err := s.db.ExecContext(ctx, `UPDATE integration_outbox SET status = 'retrying', available_at = $3,
		lease_token = NULL, lease_expires_at = NULL, last_error = $4, updated_at = now()
		WHERE id = $1 AND lease_token = $2`, item.ID, item.LeaseToken, at, cause.Error())
	return changedOne(result, err)
}

func (s *SQLoutbox) Fail(ctx context.Context, item OutboxItem, cause error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, `UPDATE integration_outbox SET
		status = CASE WHEN payload = $4::jsonb THEN 'failed' ELSE 'pending' END,
		available_at = CASE WHEN payload = $4::jsonb THEN available_at ELSE now() END,
		lease_token = NULL, lease_expires_at = NULL,
		last_error = CASE WHEN payload = $4::jsonb THEN $3 ELSE NULL END,
		updated_at = now()
		WHERE id = $1 AND lease_token = $2 RETURNING status`,
		item.ID, item.LeaseToken, cause.Error(), item.Payload).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("outbox lease 已失效")
	}
	if err != nil {
		return err
	}
	if status == "pending" {
		return tx.Commit()
	}
	if strings.HasPrefix(item.OperationKey, "conversation-title:") {
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET title_rename_status = 'failed',
			updated_at = now() WHERE id = $1 AND title_rename_status = 'scheduled'`,
			strings.TrimPrefix(item.OperationKey, "conversation-title:"))
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "desktop-thread-post:") {
		requestID := strings.TrimPrefix(item.OperationKey, "desktop-thread-post:")
		_, err = tx.ExecContext(ctx, `UPDATE desktop_thread_requests SET status = 'post_failed',
			error = $2, updated_at = now() WHERE id = $1 AND status = 'post_pending'`,
			requestID, cause.Error())
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "thread-name:") {
		controlID := strings.TrimPrefix(item.OperationKey, "thread-name:")
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
			thread_name_last_error = $2, updated_at = now()
			WHERE id = $1 AND desired_thread_name_revision > applied_thread_name_revision`,
			controlID, cause.Error())
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "conversation-lifecycle-card:") ||
		strings.HasPrefix(item.OperationKey, "conversation-lifecycle:") ||
		strings.HasPrefix(item.OperationKey, "conversation-lifecycle-delete:") {
		var sent struct {
			ConversationID string `json:"conversationId"`
			LifecycleState string `json:"lifecycleState"`
			Revision       int64  `json:"revision"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.ConversationID != "" {
			_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET
				lifecycle_projection_error = $4, updated_at = now()
				WHERE id = $1 AND lifecycle_state = $2 AND lifecycle_revision = $3`,
				sent.ConversationID, sent.LifecycleState, sent.Revision, cause.Error())
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type Dispatcher struct {
	store  OutboxStore
	remote Remote
	now    func() time.Time
	jitter func(time.Duration) time.Duration
}

func NewDispatcher(store OutboxStore, remote Remote) *Dispatcher {
	return &Dispatcher{store: store, remote: remote, now: time.Now,
		jitter: func(max time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(max) + 1)) }}
}

func (d *Dispatcher) RunOnce(ctx context.Context) (bool, error) {
	item, err := d.store.Claim(ctx, 30*time.Second)
	if err != nil || item == nil {
		return false, err
	}
	response, sendErr := d.remote.Send(ctx, *item)
	if sendErr == nil {
		return true, d.store.Complete(ctx, *item, response)
	}
	retry, wait, classified := classifyRemoteError(sendErr)
	if retry && item.Attempt < item.MaxAttempts {
		if wait <= 0 {
			wait = time.Duration(1<<(item.Attempt-1))*time.Second + d.jitter(500*time.Millisecond)
		}
		return true, d.store.Retry(ctx, *item, d.now().Add(wait), classified)
	}
	if err := d.store.Fail(ctx, *item, classified); err != nil {
		return true, err
	}
	if errors.Is(classified, ErrUnauthorized) {
		return true, classified
	}
	return true, nil
}

func classifyRemoteError(err error) (bool, time.Duration, error) {
	var restErr *disgorest.Error
	if errors.As(err, &restErr) && restErr.Response != nil {
		status := restErr.Response.StatusCode
		wait := retryAfter(restErr.Response.Header)
		switch {
		case status == http.StatusUnauthorized:
			return false, 0, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		case status == http.StatusForbidden:
			return false, 0, fmt.Errorf("%w: %v", ErrPermission, err)
		case status == http.StatusNotFound:
			return false, 0, fmt.Errorf("%w: %v", ErrResourceGone, err)
		case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
			return true, wait, err
		default:
			return false, 0, err
		}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrAmbiguousWrite) {
		return true, 0, err
	}
	return false, 0, err
}

func retryAfter(header http.Header) time.Duration {
	for _, name := range []string{"Retry-After", "X-RateLimit-Reset-After"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
				return time.Duration(seconds * float64(time.Second))
			}
			if at, err := http.ParseTime(value); err == nil {
				return max(0, time.Until(at))
			}
		}
	}
	return 0
}

func intervalLiteral(value time.Duration) string { return fmt.Sprintf("%f seconds", value.Seconds()) }

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func changedOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("outbox lease 已失效")
	}
	return nil
}
