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
	ID              string          `json:"id"`
	OperationKey    string          `json:"operationKey"`
	OperationType   string          `json:"operationType"`
	RouteKey        string          `json:"routeKey"`
	Payload         json.RawMessage `json:"payload"`
	Nonce           string          `json:"nonce,omitempty"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"maxAttempts"`
	ApplyAttempt    int             `json:"-"`
	RequestRevision int64           `json:"-"`
	Response        json.RawMessage `json:"-"`
	LeaseToken      string          `json:"-"`
	phase           outboxPhase
}

type outboxPhase uint8

const (
	outboxPhaseDeliver outboxPhase = iota
	outboxPhaseApply
)

type OutboxStore interface {
	Claim(context.Context, time.Duration) (*OutboxItem, error)
	RecordDelivery(context.Context, *OutboxItem, json.RawMessage) error
	Apply(context.Context, OutboxItem) error
	RetryDelivery(context.Context, OutboxItem, time.Time, error) error
	RetryApplication(context.Context, OutboxItem, time.Time, error) error
	FailDelivery(context.Context, OutboxItem, error) error
}

type SQLoutbox struct {
	db *sql.DB
}

func NewSQLoutbox(db *sql.DB) *SQLoutbox { return &SQLoutbox{db: db} }

func (s *SQLoutbox) Enqueue(ctx context.Context, operationKey, operationType, routeKey string, payload any, nonce string) error {
	return enqueueDiscordOutbox(ctx, s.db, operationKey, operationType, routeKey, payload, nonce)
}

func (s *SQLoutbox) EnqueueAfter(ctx context.Context, operationKey, operationType,
	routeKey string, payload any, nonce, predecessor string,
) error {
	return enqueueDiscordOutboxAfter(ctx, s.db, operationKey, operationType, routeKey,
		payload, nonce, predecessor)
}

func EnqueueTx(ctx context.Context, tx *sql.Tx, operationKey, operationType,
	routeKey string, payload any, nonce string,
) error {
	return enqueueDiscordOutbox(ctx, tx, operationKey, operationType, routeKey, payload, nonce)
}

func EnqueueTxAfter(ctx context.Context, tx *sql.Tx, operationKey, operationType,
	routeKey string, payload any, nonce, predecessor string,
) error {
	return enqueueDiscordOutboxAfter(ctx, tx, operationKey, operationType, routeKey,
		payload, nonce, predecessor)
}

type discordOutboxExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueDiscordOutbox(ctx context.Context, execer discordOutboxExecer,
	operationKey, operationType, routeKey string, payload any, nonce string,
) error {
	return enqueueDiscordOutboxAfter(ctx, execer, operationKey, operationType, routeKey,
		payload, nonce, "")
}

func enqueueDiscordOutboxAfter(ctx context.Context, execer discordOutboxExecer,
	operationKey, operationType, routeKey string, payload any, nonce, predecessor string,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	nonce = discordNonce(nonce)
	_, err = execer.ExecContext(ctx, `
		INSERT INTO integration_outbox(integration, operation_key, operation_type, route_key,
			payload, nonce, predecessor_operation_key)
		VALUES ('discord', $1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
		ON CONFLICT(integration, operation_key) DO UPDATE SET
			operation_type = EXCLUDED.operation_type, route_key = EXCLUDED.route_key,
			payload = EXCLUDED.payload, nonce = EXCLUDED.nonce,
			predecessor_operation_key = EXCLUDED.predecessor_operation_key,
			request_revision = integration_outbox.request_revision + CASE
				WHEN integration_outbox.status IN ('sending','applying','ambiguous')
					AND integration_outbox.operation_type = EXCLUDED.operation_type
					AND integration_outbox.route_key = EXCLUDED.route_key
					AND integration_outbox.payload = EXCLUDED.payload
					AND integration_outbox.nonce IS NOT DISTINCT FROM EXCLUDED.nonce THEN 0
				ELSE 1 END,
			status = CASE
				WHEN integration_outbox.status IN ('sending','applying','ambiguous')
					THEN integration_outbox.status ELSE 'pending' END,
			attempt_count = CASE
				WHEN integration_outbox.status IN ('sending','applying','ambiguous')
					THEN integration_outbox.attempt_count ELSE 0 END,
			apply_attempt_count = CASE
				WHEN integration_outbox.status IN ('sending','applying','ambiguous')
					THEN integration_outbox.apply_attempt_count ELSE 0 END,
			available_at = CASE
				WHEN integration_outbox.status = 'completed' THEN now() + interval '5 seconds'
				ELSE integration_outbox.available_at
			END,
			response = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
				THEN integration_outbox.response ELSE NULL END,
			delivered_at = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
				THEN integration_outbox.delivered_at ELSE NULL END,
			last_error = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
				THEN integration_outbox.last_error ELSE NULL END,
			updated_at = now()`, operationKey, operationType, routeKey, encoded, nonce,
		predecessor)
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
	_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET status='pending',
		available_at=now(), apply_attempt_count=0, response=NULL, delivered_at=NULL,
		inflight_revision=NULL, inflight_operation_type=NULL, inflight_route_key=NULL,
		inflight_payload=NULL, inflight_nonce=NULL, lease_token=NULL, lease_expires_at=NULL,
		last_error=NULL, updated_at=now()
		WHERE integration='discord' AND status='sending' AND lease_expires_at < now()
		AND inflight_operation_type IN ('message.update','message.delete','thread.tag.toggle')`)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET status='ambiguous',
		lease_token=NULL, lease_expires_at=NULL,
		last_error='Discord 投递租约失效；结果未知，为避免重复外部写入已停止自动重发',
		updated_at=now() WHERE integration='discord' AND status='sending'
		AND lease_expires_at < now()`)
	if err != nil {
		return nil, err
	}
	var item OutboxItem
	var id uuid.UUID
	var status string
	var response []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, operation_key, status,
			CASE WHEN status='applying' THEN inflight_operation_type ELSE operation_type END,
			CASE WHEN status='applying' THEN inflight_route_key ELSE route_key END,
			CASE WHEN status='applying' THEN inflight_payload ELSE payload END,
			CASE WHEN status='applying' THEN COALESCE(inflight_nonce,'') ELSE COALESCE(nonce,'') END,
			CASE WHEN status='applying' THEN attempt_count ELSE attempt_count + 1 END,
			max_attempts,
			CASE WHEN status='applying' THEN apply_attempt_count + 1 ELSE 0 END,
			CASE WHEN status='applying' THEN inflight_revision ELSE request_revision END,
			response
		FROM integration_outbox
		WHERE integration = 'discord' AND available_at <= now() AND (
			(status IN ('pending', 'retrying') AND lease_token IS NULL)
			OR (status='applying' AND (lease_expires_at IS NULL OR lease_expires_at < now())))
		AND (predecessor_operation_key IS NULL OR EXISTS(
			SELECT 1 FROM integration_outbox predecessor
			WHERE predecessor.integration=integration_outbox.integration
			  AND predecessor.operation_key=integration_outbox.predecessor_operation_key
			  AND predecessor.status='completed'))
		ORDER BY available_at, created_at, enqueue_sequence
		FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&id, &item.OperationKey, &status, &item.OperationType, &item.RouteKey,
			&item.Payload, &item.Nonce, &item.Attempt, &item.MaxAttempts,
			&item.ApplyAttempt, &item.RequestRevision, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	item.Response = response
	token, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	item.ID, item.LeaseToken = id.String(), token
	if status == "applying" {
		item.phase = outboxPhaseApply
		_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET
			apply_attempt_count=$2, lease_token=$3,
			lease_expires_at=now()+$4::interval, updated_at=now() WHERE id=$1`,
			id, item.ApplyAttempt, token, intervalLiteral(lease))
	} else {
		item.phase = outboxPhaseDeliver
		_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET status='sending',
			attempt_count=$2, apply_attempt_count=0, inflight_revision=$3,
			inflight_operation_type=$4, inflight_route_key=$5, inflight_payload=$6,
			inflight_nonce=NULLIF($7,''), response=NULL, delivered_at=NULL,
			lease_token=$8, lease_expires_at=now()+$9::interval,
			last_error=NULL, updated_at=now() WHERE id=$1`, id, item.Attempt,
			item.RequestRevision, item.OperationType, item.RouteKey, item.Payload,
			item.Nonce, token, intervalLiteral(lease))
	}
	if err != nil {
		return nil, err
	}
	return &item, tx.Commit()
}

func (s *SQLoutbox) RecordDelivery(ctx context.Context, item *OutboxItem,
	response json.RawMessage,
) error {
	result, err := s.db.ExecContext(ctx, `UPDATE integration_outbox SET
		status='applying', response=$4, delivered_at=now(), apply_attempt_count=1,
		available_at=now(), last_error=NULL, updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='sending' AND inflight_revision=$3`,
		item.ID, item.LeaseToken, item.RequestRevision, nullableJSON(response))
	if err := changedOne(result, err); err != nil {
		return err
	}
	item.phase = outboxPhaseApply
	item.ApplyAttempt = 1
	item.Response = response
	return nil
}

// ResolveAmbiguousDelivery 由运维在远端结果已经人工对账后恢复本地应用阶段。
// 它只接受 ambiguous，绝不会主动重发外部请求。
func (s *SQLoutbox) ResolveAmbiguousDelivery(ctx context.Context, operationKey string,
	response json.RawMessage,
) (*OutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var item OutboxItem
	var id uuid.UUID
	var nonce sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, operation_key, inflight_operation_type,
		inflight_route_key, inflight_payload, inflight_nonce, attempt_count, max_attempts,
		apply_attempt_count+1, inflight_revision
		FROM integration_outbox WHERE integration='discord' AND operation_key=$1
		AND status='ambiguous' FOR UPDATE`, operationKey).Scan(&id, &item.OperationKey,
		&item.OperationType, &item.RouteKey, &item.Payload, &nonce, &item.Attempt,
		&item.MaxAttempts, &item.ApplyAttempt, &item.RequestRevision)
	if err != nil {
		return nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	item.ID, item.Nonce, item.LeaseToken = id.String(), nonce.String, token
	item.Response, item.phase = response, outboxPhaseApply
	result, err := tx.ExecContext(ctx, `UPDATE integration_outbox SET status='applying',
		response=$3, delivered_at=COALESCE(delivered_at,now()), apply_attempt_count=$4,
		available_at=now(), lease_token=$5, lease_expires_at=now()+interval '5 minutes',
		last_error=NULL, updated_at=now() WHERE id=$1 AND inflight_revision=$2`,
		id, item.RequestRevision, nullableJSON(response), item.ApplyAttempt, token)
	if err := changedOne(result, err); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *SQLoutbox) Apply(ctx context.Context, item OutboxItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var officialBindingID uuid.UUID
	projectionKey, projectionExists := "", false
	if strings.HasPrefix(item.OperationKey, "projection:") {
		projectionKey = strings.TrimPrefix(item.OperationKey, "projection:")
		var locked int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT 1 FROM discord_projections
			WHERE projection_key = $1 FOR UPDATE), 0)`,
			projectionKey).Scan(&locked); err != nil {
			return err
		}
		projectionExists = locked == 1
	}
	projectionGone := projectionKey != "" && !projectionExists
	result, err := tx.ExecContext(ctx, `UPDATE integration_outbox SET
		status = CASE WHEN request_revision=$3 OR $4 THEN 'completed' ELSE 'pending' END,
		available_at = CASE WHEN request_revision=$3 OR $4 THEN available_at ELSE now()+interval '5 seconds' END,
		response = CASE WHEN request_revision=$3 OR $4 THEN response ELSE NULL END,
		delivered_at = CASE WHEN request_revision=$3 OR $4 THEN delivered_at ELSE NULL END,
		attempt_count = CASE WHEN request_revision=$3 OR $4 THEN attempt_count ELSE 0 END,
		apply_attempt_count = CASE WHEN request_revision=$3 OR $4 THEN apply_attempt_count ELSE 0 END,
		inflight_revision=NULL, inflight_operation_type=NULL, inflight_route_key=NULL,
		inflight_payload=NULL, inflight_nonce=NULL,
		lease_token=NULL, lease_expires_at=NULL, last_error=NULL, updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='applying' AND inflight_revision=$3`,
		item.ID, item.LeaseToken, item.RequestRevision, projectionGone)
	if err := changedOne(result, err); err != nil {
		return err
	}
	response := item.Response
	if projectionKey != "" {
		var value struct {
			ThreadID  string `json:"threadId"`
			MessageID string `json:"messageId"`
		}
		_ = json.Unmarshal(response, &value)
		if !projectionExists {
			if value.MessageID != "" && (item.OperationType == "message.create" ||
				item.OperationType == "forum.post.create") {
				var delivered struct {
					ChannelID string `json:"channelId"`
				}
				_ = json.Unmarshal(item.Payload, &delivered)
				if value.ThreadID != "" {
					delivered.ChannelID = value.ThreadID
				}
				if delivered.ChannelID == "" {
					return errors.New("已删除 Projection 的 Discord 创建结果缺少频道 ID")
				}
				if err := enqueueDiscordOutbox(ctx, tx,
					"projection-orphan-delete:"+projectionKey+":"+value.MessageID,
					"message.delete", "channels/"+delivered.ChannelID+"/messages/"+value.MessageID,
					map[string]string{"channelId": delivered.ChannelID, "messageId": value.MessageID}, ""); err != nil {
					return err
				}
			}
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE discord_projections SET
			resource_id = COALESCE(NULLIF($3, ''), resource_id),
			message_id = COALESCE(NULLIF($2, ''), message_id),
			applied_version = CASE WHEN o.status = 'completed' THEN desired_version ELSE applied_version END,
			applied_at = CASE WHEN o.status = 'completed' THEN now() ELSE applied_at END,
			last_error = NULL, updated_at = now()
			FROM integration_outbox o WHERE projection_key = $1 AND o.id = $4`,
				projectionKey, value.MessageID, value.ThreadID, item.ID)
			if err != nil {
				return err
			}
			if value.MessageID != "" && (item.OperationType == "message.create" ||
				item.OperationType == "forum.post.create") {
				var delivered struct {
					ChannelID string `json:"channelId"`
				}
				_ = json.Unmarshal(item.Payload, &delivered)
				if value.ThreadID != "" {
					delivered.ChannelID = value.ThreadID
				}
				if delivered.ChannelID == "" {
					return errors.New("待更新 Projection 的 Discord 创建结果缺少频道 ID")
				}
				_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET
					operation_type='message.update',nonce=NULL,
					route_key='channels/'||$2::text||'/messages',
					payload=payload||jsonb_build_object(
						'channelId',$2::text,'messageId',$3::text),
					updated_at=now() WHERE id=$1 AND status='pending'`, item.ID,
					delivered.ChannelID, value.MessageID)
				if err != nil {
					return err
				}
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
	if strings.HasPrefix(item.OperationKey, "official-thread-post:") {
		officialBindingID, err = s.completeOfficialThreadPost(ctx, tx, item, response)
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(item.OperationKey, "task-log:") || strings.HasPrefix(item.OperationKey, "task-card:") {
		var sent struct {
			WorkItemID string `json:"workItemId"`
			State      string `json:"state"`
		}
		var value struct {
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(item.Payload, &sent) == nil && sent.WorkItemID != "" {
			if strings.HasPrefix(item.OperationKey, "task-card:") {
				_ = json.Unmarshal(response, &value)
			}
			_, err = tx.ExecContext(ctx, `UPDATE discord_task_posts SET last_state = $2,
				starter_message_id=COALESCE(NULLIF($3,''),starter_message_id),
				last_projected_at = now() WHERE work_item_id = $1`, sent.WorkItemID,
				sent.State, value.MessageID)
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
	if item.OperationType == "message.update" {
		var value struct {
			MessageID string `json:"messageId"`
		}
		var previous struct {
			ChannelID string `json:"channelId"`
			MessageID string `json:"messageId"`
		}
		_ = json.Unmarshal(response, &value)
		_ = json.Unmarshal(item.Payload, &previous)
		if value.MessageID != "" && previous.ChannelID != "" && previous.MessageID != "" &&
			value.MessageID != previous.MessageID {
			if !messageReplacementReferenceSupported(item.OperationKey) {
				return fmt.Errorf("discord 替代消息缺少本地引用回写: %s", item.OperationKey)
			}
			_, err = tx.ExecContext(ctx, `UPDATE integration_outbox SET
				operation_type='message.update', nonce=NULL,
				route_key='channels/' || $2 || '/messages/' || $3,
				payload=payload || jsonb_build_object('channelId',$2,'messageId',$3),
				request_revision=request_revision+1, updated_at=now()
				WHERE id=$1 AND status='pending'`, item.ID, previous.ChannelID, value.MessageID)
			if err != nil {
				return err
			}
			deleteKey := "message-replaced-delete:" + item.OperationKey + ":" + previous.MessageID
			if strings.HasPrefix(item.OperationKey, "projection:") {
				deleteKey = "projection-replaced-delete:" + projectionKey + ":" + previous.MessageID
			}
			if err := enqueueDiscordOutbox(ctx, tx, deleteKey, "message.delete",
				"channels/"+previous.ChannelID+"/messages/"+previous.MessageID,
				map[string]string{"channelId": previous.ChannelID, "messageId": previous.MessageID}, ""); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if officialBindingID != uuid.Nil {
		return ReplayOfficialThreadProjection(ctx, s.db, officialBindingID)
	}
	return nil
}

func messageReplacementReferenceSupported(operationKey string) bool {
	prefixes := []string{
		"projection:", "task-card:", "conversation-lifecycle-card:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(operationKey, prefix) {
			return true
		}
	}
	return false
}

func (s *SQLoutbox) RetryDelivery(ctx context.Context, item OutboxItem,
	at time.Time, cause error,
) error {
	result, err := s.db.ExecContext(ctx, `UPDATE integration_outbox SET status='retrying',
		attempt_count=CASE WHEN request_revision=$3 THEN attempt_count ELSE 0 END,
		available_at=$4, inflight_revision=NULL, inflight_operation_type=NULL,
		inflight_route_key=NULL, inflight_payload=NULL, inflight_nonce=NULL,
		lease_token=NULL, lease_expires_at=NULL, last_error=$5, updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='sending' AND inflight_revision=$3`,
		item.ID, item.LeaseToken, item.RequestRevision, at, cause.Error())
	return changedOne(result, err)
}

func (s *SQLoutbox) RetryApplication(ctx context.Context, item OutboxItem,
	at time.Time, cause error,
) error {
	result, err := s.db.ExecContext(ctx, `UPDATE integration_outbox SET
		available_at=$3, lease_token=NULL, lease_expires_at=NULL,
		last_error=$4, updated_at=now()
		WHERE id=$1 AND lease_token=$2 AND status='applying'`,
		item.ID, item.LeaseToken, at, cause.Error())
	return changedOne(result, err)
}

func (s *SQLoutbox) FailDelivery(ctx context.Context, item OutboxItem, cause error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, `UPDATE integration_outbox SET
		status = CASE WHEN request_revision=$4 THEN 'failed' ELSE 'pending' END,
		available_at = CASE WHEN request_revision=$4 THEN available_at ELSE now() END,
		attempt_count = CASE WHEN request_revision=$4 THEN attempt_count ELSE 0 END,
		apply_attempt_count=0, response=NULL, delivered_at=NULL,
		inflight_revision=NULL, inflight_operation_type=NULL, inflight_route_key=NULL,
		inflight_payload=NULL, inflight_nonce=NULL,
		lease_token = NULL, lease_expires_at = NULL,
		last_error = CASE WHEN request_revision=$4 THEN $3 ELSE NULL END,
		updated_at = now()
		WHERE id=$1 AND lease_token=$2 AND status='sending' AND inflight_revision=$4
		RETURNING status`, item.ID, item.LeaseToken, cause.Error(), item.RequestRevision).
		Scan(&status)
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
	if item.phase == outboxPhaseApply {
		return true, d.apply(ctx, *item)
	}
	response, sendErr := d.remote.Send(ctx, *item)
	if sendErr == nil {
		if err := d.store.RecordDelivery(ctx, item, response); err != nil {
			return true, err
		}
		return true, d.apply(ctx, *item)
	}
	retry, wait, classified := classifyRemoteError(sendErr)
	if retry && item.Attempt < item.MaxAttempts {
		if wait <= 0 {
			wait = time.Duration(1<<(item.Attempt-1))*time.Second + d.jitter(500*time.Millisecond)
		}
		return true, d.store.RetryDelivery(ctx, *item, d.now().Add(wait), classified)
	}
	if err := d.store.FailDelivery(ctx, *item, classified); err != nil {
		return true, err
	}
	if errors.Is(classified, ErrUnauthorized) {
		return true, classified
	}
	return true, nil
}

func (d *Dispatcher) apply(ctx context.Context, item OutboxItem) error {
	if err := d.store.Apply(ctx, item); err != nil {
		shift := item.ApplyAttempt - 1
		if shift < 0 {
			shift = 0
		}
		if shift > 6 {
			shift = 6
		}
		wait := time.Duration(1<<shift)*time.Second + d.jitter(500*time.Millisecond)
		if retryErr := d.store.RetryApplication(ctx, item, d.now().Add(wait), err); retryErr != nil {
			return errors.Join(err, retryErr)
		}
		return err
	}
	return nil
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
