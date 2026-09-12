package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/live"
	"go.uber.org/zap"
)

type liveRuntime struct {
	mu       sync.Mutex
	sideband live.Sideband
	cancel   context.CancelFunc
}

type liveManager struct {
	db          *sql.DB
	provider    live.Provider
	logger      *zap.Logger
	mu          sync.Mutex
	runtimes    map[uuid.UUID]*liveRuntime
	transcripts map[string]string
}

func newLiveManager(db *sql.DB, provider live.Provider, logger *zap.Logger) *liveManager {
	return &liveManager{db: db, provider: provider, logger: logger, runtimes: make(map[uuid.UUID]*liveRuntime), transcripts: make(map[string]string)}
}

func (m *liveManager) Start(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `SELECT id,remote_session_id FROM live_sessions
		WHERE status IN ('active','sideband_disconnected','recovering') AND remote_session_id <> ''`)
	if err != nil {
		return fmt.Errorf("扫描 Live session: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uuid.UUID
		var remoteID string
		if err := rows.Scan(&id, &remoteID); err != nil {
			return err
		}
		m.attach(ctx, id, remoteID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	<-ctx.Done()
	m.stopAll()
	return ctx.Err()
}

func (m *liveManager) attach(parent context.Context, sessionID uuid.UUID, remoteID string) {
	m.mu.Lock()
	if _, exists := m.runtimes[sessionID]; exists {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &liveRuntime{cancel: cancel}
	m.runtimes[sessionID] = runtime
	m.mu.Unlock()
	go m.run(ctx, sessionID, remoteID, runtime)
}

func (m *liveManager) run(ctx context.Context, sessionID uuid.UUID, remoteID string, runtime *liveRuntime) {
	defer func() {
		runtime.mu.Lock()
		if runtime.sideband != nil {
			_ = runtime.sideband.Close()
			runtime.sideband = nil
		}
		runtime.mu.Unlock()
		m.mu.Lock()
		delete(m.runtimes, sessionID)
		m.mu.Unlock()
	}()
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		sideband, err := m.provider.AttachSideband(ctx, remoteID)
		if err != nil {
			if errors.Is(err, live.ErrSessionExpired) {
				_ = m.updateSessionStatus(context.Background(), sessionID, "expired", err.Error())
				_, _ = m.db.ExecContext(context.Background(), `UPDATE live_conversations
					SET active_session_id=NULL,updated_at=now() WHERE active_session_id=$1`, sessionID)
				return
			}
			_ = m.updateSessionStatus(ctx, sessionID, "sideband_disconnected", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			continue
		}
		backoff = time.Second
		runtime.mu.Lock()
		runtime.sideband = sideband
		runtime.mu.Unlock()
		_ = m.updateSessionStatus(ctx, sessionID, "active", "")
		for {
			var event map[string]any
			if err := sideband.ReadJSON(ctx, &event); err != nil {
				_ = sideband.Close()
				if ctx.Err() != nil {
					return
				}
				runtime.mu.Lock()
				if runtime.sideband == sideband {
					runtime.sideband = nil
				}
				runtime.mu.Unlock()
				_ = m.flushTranscripts(context.Background(), sessionID)
				_ = m.updateSessionStatus(context.Background(), sessionID, "sideband_disconnected", err.Error())
				break
			}
			if err := m.persistEvent(context.Background(), sessionID, "server", event); err != nil && m.logger != nil {
				m.logger.Warn("Live sideband 事件持久化失败", zap.String("sessionId", sessionID.String()), zap.Error(err))
			}
			if eventType(event) == "session.closed" {
				_ = m.flushTranscripts(context.Background(), sessionID)
				return
			}
		}
	}
}

func (m *liveManager) stopAll() {
	m.mu.Lock()
	runtimes := make([]*liveRuntime, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtimes = append(runtimes, runtime)
	}
	m.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.cancel()
		runtime.mu.Lock()
		if runtime.sideband != nil {
			_ = runtime.sideband.Close()
		}
		runtime.mu.Unlock()
	}
}

func (m *liveManager) stopSession(sessionID uuid.UUID) {
	m.mu.Lock()
	runtime := m.runtimes[sessionID]
	m.mu.Unlock()
	if runtime == nil {
		return
	}
	runtime.cancel()
	runtime.mu.Lock()
	if runtime.sideband != nil {
		_ = runtime.sideband.Close()
	}
	runtime.mu.Unlock()
}

func (m *liveManager) sideband(sessionID uuid.UUID) (live.Sideband, bool) {
	m.mu.Lock()
	runtime, ok := m.runtimes[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.sideband, runtime.sideband != nil
}

func (m *liveManager) sendClose(ctx context.Context, sessionID uuid.UUID) error {
	sideband, ok := m.sideband(sessionID)
	if !ok {
		return errors.New("Live sideband 尚未连接")
	}
	return sideband.WriteJSON(ctx, map[string]string{"type": "session.close"})
}

func (m *liveManager) updateSessionStatus(ctx context.Context, id uuid.UUID, status, lastError string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE live_sessions SET status=$2,last_error=NULLIF($3,''),
		started_at=CASE WHEN $2='active' AND started_at IS NULL THEN now() ELSE started_at END,
		disconnected_at=CASE WHEN $2='sideband_disconnected' AND disconnected_at IS NULL THEN now() ELSE disconnected_at END,
		expired_at=CASE WHEN $2='expired' AND expired_at IS NULL THEN now() ELSE expired_at END,
		updated_at=now() WHERE id=$1`, id, status, lastError)
	return err
}

func eventType(event map[string]any) string {
	value, _ := event["type"].(string)
	return strings.TrimSpace(value)
}
func eventID(event map[string]any) string {
	value, _ := event["id"].(string)
	return strings.TrimSpace(value)
}
func eventDedupeKey(event map[string]any) string {
	if id := eventID(event); id != "" {
		return id
	}
	encoded, _ := json.Marshal(event)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m *liveManager) persistEvent(ctx context.Context, sessionID uuid.UUID, direction string, event map[string]any) error {
	typ := eventType(event)
	if typ == "" {
		return errors.New("Live 事件缺少 type")
	}
	dedupe := eventDedupeKey(event)
	payload, err := json.Marshal(sanitizeLivePayload(event, strings.Contains(strings.ToLower(typ), "audio")))
	if err != nil {
		return err
	}
	result, err := m.db.ExecContext(ctx, `INSERT INTO live_events(live_session_id,direction,event_type,event_id,dedupe_key,payload)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT(live_session_id,dedupe_key) DO NOTHING`, sessionID, direction, typ, eventID(event), dedupe, payload)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	return m.projectEvent(ctx, sessionID, typ, event)
}

func sanitizeLivePayload(value any, audio bool) any {
	if audio {
		result := map[string]any{}
		if object, ok := value.(map[string]any); ok {
			if typ, ok := object["type"].(string); ok {
				result["type"] = typ
			}
			if id, ok := object["id"].(string); ok {
				result["id"] = id
			}
			if raw, ok := object["delta"].(string); ok {
				result["audioBytes"] = len(raw) * 3 / 4
			}
		}
		return result
	}
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, child := range item {
			lower := strings.ToLower(key)
			if lower == "audio" || lower == "audio_base64" || lower == "pcm" {
				continue
			}
			result[key] = sanitizeLivePayload(child, false)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = sanitizeLivePayload(child, false)
		}
		return result
	default:
		return value
	}
}

func (m *liveManager) projectEvent(ctx context.Context, sessionID uuid.UUID, typ string, event map[string]any) error {
	if typ == "session.started" {
		return m.updateSessionStatus(ctx, sessionID, "active", "")
	}
	if typ == "session.closed" {
		_, err := m.db.ExecContext(ctx, `UPDATE live_sessions SET status='closed',closed_at=COALESCE(closed_at,now()),updated_at=now() WHERE id=$1`, sessionID)
		if err != nil {
			return err
		}
		_, err = m.db.ExecContext(ctx, `UPDATE live_conversations SET active_session_id=NULL,updated_at=now() WHERE active_session_id=$1`, sessionID)
		return err
	}
	if typ == "error" {
		return m.updateSessionStatus(ctx, sessionID, "active", eventText(event))
	}
	if isTranscriptDelta(typ) {
		key := transcriptKey(sessionID, typ, event)
		m.mu.Lock()
		m.transcripts[key] += eventText(event)
		m.mu.Unlock()
		return nil
	}
	if isInputTranscriptDone(typ) || isOutputTranscriptDone(typ) {
		role := "assistant"
		if isInputTranscriptDone(typ) {
			role = "user"
		}
		key := transcriptKey(sessionID, typ, event)
		text := eventText(event)
		m.mu.Lock()
		if text == "" {
			text = m.transcripts[key]
		}
		delete(m.transcripts, key)
		m.mu.Unlock()
		if text == "" {
			return nil
		}
		return m.appendLiveMessage(ctx, sessionID, role, text, eventDedupeKey(event))
	}
	return nil
}

func (m *liveManager) appendLiveMessage(ctx context.Context, sessionID uuid.UUID, role, text, sourceEventID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM live_sessions WHERE id=$1 FOR SHARE`, sessionID).Scan(&conversationID); err != nil {
		return err
	}
	// 串行化同一 conversation 的 sequence 分配，避免同时完成的转写互相覆盖。
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, conversationID.String()); err != nil {
		return err
	}
	var inserted bool
	if err := tx.QueryRowContext(ctx, `INSERT INTO live_messages(conversation_id,sequence,role,text,source_session_id,source_event_id)
		SELECT $1,COALESCE(MAX(sequence),0)+1,$2,$3,$4,$5 FROM live_messages
		RETURNING true`, conversationID, role, text, sessionID, sourceEventID).Scan(&inserted); err != nil {
		return err
	}
	if inserted {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversations SET context_revision=context_revision+1,updated_at=now() WHERE id=$1`, conversationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *liveManager) flushTranscripts(ctx context.Context, sessionID uuid.UUID) error {
	prefix := sessionID.String() + ":"
	type pendingTranscript struct {
		key, role, text string
	}
	m.mu.Lock()
	pending := make([]pendingTranscript, 0)
	for key, text := range m.transcripts {
		if !strings.HasPrefix(key, prefix) || strings.TrimSpace(text) == "" {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(key, prefix), ":", 2)
		if len(parts) != 2 {
			continue
		}
		pending = append(pending, pendingTranscript{key: key, role: parts[0], text: text})
		delete(m.transcripts, key)
	}
	m.mu.Unlock()
	for _, item := range pending {
		if err := m.appendLiveMessage(ctx, sessionID, item.role, item.text, "transcript:"+item.key); err != nil {
			return err
		}
	}
	return nil
}

func isTranscriptDelta(typ string) bool {
	return (strings.Contains(typ, "input_transcript") || strings.Contains(typ, "output_transcript") || strings.Contains(typ, "output_audio_transcript") || strings.Contains(typ, "output_text")) && strings.HasSuffix(typ, ".delta")
}
func transcriptKey(sessionID uuid.UUID, typ string, event map[string]any) string {
	role := "assistant"
	if strings.Contains(typ, "input_") {
		role = "user"
	}
	stream := strings.TrimSuffix(strings.TrimSuffix(typ, ".delta"), ".done")
	stream = strings.TrimSuffix(stream, ".completed")
	for _, name := range []string{"item_id", "response_id", "itemId", "responseId"} {
		if value, ok := event[name].(string); ok && value != "" {
			return sessionID.String() + ":" + role + ":" + value
		}
	}
	return sessionID.String() + ":" + role + ":" + stream
}

func isInputTranscriptDone(typ string) bool {
	return strings.Contains(typ, "input_transcript") && strings.HasSuffix(typ, ".done") || strings.Contains(typ, "input_audio_transcription") && strings.HasSuffix(typ, ".completed")
}
func isOutputTranscriptDone(typ string) bool {
	return (strings.Contains(typ, "output_transcript") || strings.Contains(typ, "output_audio_transcript")) && strings.HasSuffix(typ, ".done") || strings.Contains(typ, "output_text") && strings.HasSuffix(typ, ".done")
}
func eventText(event map[string]any) string {
	for _, key := range []string{"text", "transcript", "delta", "message"} {
		if value, ok := event[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	if object, ok := event["error"].(map[string]any); ok {
		if value, ok := object["message"].(string); ok {
			return value
		}
	}
	return ""
}
