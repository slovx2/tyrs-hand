package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/live"
	"go.uber.org/zap"
)

type liveRuntime struct {
	mu       sync.Mutex
	writeMu  sync.Mutex
	sideband live.Sideband
	cancel   context.CancelFunc
}

type transcriptMutation struct {
	append map[string]string
	clear  []string
}

type liveManager struct {
	db          *sql.DB
	provider    live.Provider
	logger      *zap.Logger
	mu          sync.Mutex
	runtimes    map[uuid.UUID]*liveRuntime
	transcripts map[string]string
	rootCtx     context.Context
	ready       chan struct{}
}

func newLiveManager(db *sql.DB, provider live.Provider, logger *zap.Logger) *liveManager {
	return &liveManager{
		db: db, provider: provider, logger: logger,
		runtimes:    make(map[uuid.UUID]*liveRuntime),
		transcripts: make(map[string]string),
		ready:       make(chan struct{}),
	}
}

func (m *liveManager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.rootCtx != nil {
		m.mu.Unlock()
		return errors.New("Live manager 已启动")
	}
	m.rootCtx = ctx
	close(m.ready)
	m.mu.Unlock()

	// A process exit can interrupt the short window between inserting a local
	// session and receiving the provider ID. Such a row cannot be resumed and
	// must not reserve the conversation forever after restart.
	const interruptedCreatingError = "Control 在 Live session 创建期间退出"
	if _, err := m.db.ExecContext(ctx, `UPDATE live_sessions
		SET status='failed',last_error=$1,updated_at=now()
		WHERE status='creating' AND remote_session_id=''`, interruptedCreatingError); err != nil {
		return fmt.Errorf("清理中断的 Live session: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE live_conversations
		SET active_session_id=NULL,status='active',updated_at=now()
		WHERE active_session_id IN (
			SELECT id FROM live_sessions WHERE status='failed' AND last_error=$1
		)`, interruptedCreatingError); err != nil {
		return fmt.Errorf("释放中断的 Live conversation: %w", err)
	}

	rows, err := m.db.QueryContext(ctx, `SELECT id,remote_session_id FROM live_sessions
		WHERE status IN ('creating','active','sideband_disconnected','recovering','closing','close_timeout')
		AND remote_session_id <> ''`)
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
		if err := m.restoreTranscripts(ctx, id); err != nil {
			m.logWarn("恢复 Live transcript 累积失败", id, err)
		}
		if err := m.attach(ctx, id, remoteID); err != nil {
			m.logWarn("恢复 Live sideband 失败", id, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	<-ctx.Done()
	m.stopAll()
	return ctx.Err()
}

func (m *liveManager) rootContext() (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rootCtx == nil {
		return nil, errors.New("Live manager 尚未启动")
	}
	return m.rootCtx, nil
}

func (m *liveManager) waitRootContext(ctx context.Context) (context.Context, error) {
	for {
		m.mu.Lock()
		root := m.rootCtx
		ready := m.ready
		m.mu.Unlock()
		if root != nil {
			return root, nil
		}
		if ready == nil {
			return nil, errors.New("Live manager 尚未初始化")
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (m *liveManager) attach(waitCtx context.Context, sessionID uuid.UUID, remoteID string) error {
	parent, err := m.waitRootContext(waitCtx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, exists := m.runtimes[sessionID]; exists {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &liveRuntime{cancel: cancel}
	m.runtimes[sessionID] = runtime
	m.mu.Unlock()
	go m.run(ctx, sessionID, remoteID, runtime)
	return nil
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
		if current, ok := m.runtimes[sessionID]; ok && current == runtime {
			delete(m.runtimes, sessionID)
		}
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
				_ = m.updateSessionStatus(ctx, sessionID, "expired", err.Error())
				return
			}
			if errors.Is(err, live.ErrSidebandBinary) {
				_ = m.updateSessionStatus(ctx, sessionID, "failed", err.Error())
				return
			}
			_ = m.updateSessionStatus(ctx, sessionID, "sideband_disconnected", err.Error())
			if !waitBackoff(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = time.Second
		runtime.mu.Lock()
		runtime.sideband = sideband
		runtime.mu.Unlock()
		_ = m.markSidebandConnected(ctx, sessionID)
		for {
			var event map[string]any
			if err := sideband.ReadJSON(ctx, &event); err != nil {
				_ = sideband.Close()
				runtime.mu.Lock()
				if runtime.sideband == sideband {
					runtime.sideband = nil
				}
				runtime.mu.Unlock()
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, live.ErrSidebandBinary) {
					m.recordSidebandProtocolEvent(ctx, sessionID, map[string]any{
						"type":      "sideband.binary",
						"sizeBytes": sidebandBinarySize(err),
					})
					_ = m.updateSessionStatus(ctx, sessionID, "failed", err.Error())
					return
				}
				if errors.Is(err, live.ErrSidebandInvalidJSON) {
					_ = m.updateSessionStatus(ctx, sessionID, "failed", err.Error())
					return
				}
				_ = m.updateSessionStatus(ctx, sessionID, "sideband_disconnected", err.Error())
				break
			}
			if err := m.persistEvent(ctx, sessionID, "server", event); err != nil {
				m.logWarn("Live sideband 事件持久化失败", sessionID, err)
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if eventType(event) == "session.closed" {
				return
			}
		}
	}
}

func waitBackoff(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current >= 30*time.Second {
		return 30 * time.Second
	}
	current *= 2
	if current > 30*time.Second {
		return 30 * time.Second
	}
	return current
}

func isTerminalLiveSessionStatus(status string) bool {
	switch status {
	case "closed", "expired", "failed", "replaced":
		return true
	default:
		return false
	}
}

func sidebandBinarySize(err error) int {
	var binaryErr *live.SidebandBinaryError
	if errors.As(err, &binaryErr) {
		return binaryErr.Size
	}
	return 0
}

func (m *liveManager) recordSidebandProtocolEvent(ctx context.Context, sessionID uuid.UUID, event map[string]any) {
	if err := m.persistEvent(ctx, sessionID, "server", event); err != nil {
		m.logWarn("Live sideband 协议事件持久化失败", sessionID, err)
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
	delete(m.runtimes, sessionID)
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

func (m *liveManager) waitSideband(ctx context.Context, sessionID uuid.UUID) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, ok := m.sideband(sessionID); ok {
			return nil
		}
		var status string
		err := m.db.QueryRowContext(ctx, `SELECT status FROM live_sessions WHERE id=$1`, sessionID).Scan(&status)
		if err != nil {
			return err
		}
		switch status {
		case "expired", "failed", "replaced":
			return fmt.Errorf("Live session 进入终态 %q", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *liveManager) sendClose(ctx context.Context, sessionID uuid.UUID) error {
	runtime, ok := m.runtime(sessionID)
	if !ok {
		return errors.New("Live sideband 尚未连接")
	}
	runtime.mu.Lock()
	sideband := runtime.sideband
	runtime.mu.Unlock()
	if sideband == nil {
		return errors.New("Live sideband 尚未连接")
	}
	runtime.writeMu.Lock()
	defer runtime.writeMu.Unlock()
	if err := m.persistEvent(ctx, sessionID, "client", map[string]any{"type": "session.close"}); err != nil {
		return err
	}
	return sideband.WriteJSON(ctx, map[string]string{"type": "session.close"})
}

func (m *liveManager) runtime(sessionID uuid.UUID) (*liveRuntime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.runtimes[sessionID]
	return runtime, ok
}

func (m *liveManager) markSidebandConnected(ctx context.Context, id uuid.UUID) error {
	return m.updateSessionStatus(ctx, id, "active", "")
}

func (m *liveManager) updateSessionStatus(ctx context.Context, id uuid.UUID, status, lastError string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM live_sessions WHERE id=$1 FOR UPDATE`, id).Scan(&current); err != nil {
		return err
	}
	// A runtime that is being stopped can still finish one in-flight read. Once
	// a session is terminal, that stale runtime must not revive or rewrite it.
	if isTerminalLiveSessionStatus(current) {
		return tx.Commit()
	}
	// A transport interruption must not turn an explicit close into a reconnectable session.
	effective := status
	if (current == "closing" || current == "close_timeout") &&
		status != "closed" && status != "expired" && status != "failed" {
		effective = current
	}
	if _, err := tx.ExecContext(ctx, `UPDATE live_sessions SET status=$2,last_error=NULLIF($3,''),
		started_at=CASE WHEN $2='active' AND started_at IS NULL THEN now() ELSE started_at END,
		disconnected_at=CASE WHEN $2='sideband_disconnected' THEN COALESCE(disconnected_at,now()) ELSE disconnected_at END,
		expired_at=CASE WHEN $2='expired' THEN COALESCE(expired_at,now()) ELSE expired_at END,
		closed_at=CASE WHEN $2='closed' THEN COALESCE(closed_at,now()) ELSE closed_at END,
		updated_at=now() WHERE id=$1`, id, effective, lastError); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE live_conversations SET
		active_session_id=CASE WHEN active_session_id=$1 AND $2 IN ('expired','failed','closed') THEN NULL ELSE active_session_id END,
		status=CASE WHEN active_session_id=$1 THEN CASE
			WHEN $2='sideband_disconnected' THEN 'sideband_disconnected'
			WHEN $2 IN ('closing','close_timeout','recovering') THEN $2
			WHEN $2 IN ('expired','failed','closed') THEN 'active'
			ELSE 'active' END ELSE status END,
		updated_at=now() WHERE active_session_id=$1`, id, effective); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *liveManager) eventProjectUpdate(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, status string) error {
	where := `WHERE id=$1`
	if status == "active" {
		where += ` AND status NOT IN ('closing','close_timeout','failed','expired','replaced')`
	} else if status == "closed" {
		where += ` AND status NOT IN ('closed','failed','expired','replaced')`
	}
	_, err := tx.ExecContext(ctx, `UPDATE live_sessions SET status=$2,
		started_at=CASE WHEN $2='active' AND started_at IS NULL THEN now() ELSE started_at END,
		disconnected_at=CASE WHEN $2='sideband_disconnected' THEN COALESCE(disconnected_at,now()) ELSE disconnected_at END,
		expired_at=CASE WHEN $2='expired' THEN COALESCE(expired_at,now()) ELSE expired_at END,
		closed_at=CASE WHEN $2='closed' THEN COALESCE(closed_at,now()) ELSE closed_at END,
		updated_at=now() `+where, sessionID, status)
	return err
}

func eventType(event map[string]any) string {
	value, _ := event["type"].(string)
	return strings.TrimSpace(value)
}

func eventID(event map[string]any) string {
	for _, key := range []string{"id", "event_id", "eventId"} {
		if value, ok := event[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	payload, err := json.Marshal(sanitizeLivePayload(event, isAudioEvent(typ)))
	if err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM live_sessions WHERE id=$1 FOR UPDATE`, sessionID).Scan(&currentStatus); err != nil {
		return err
	}
	var inserted bool
	err = tx.QueryRowContext(ctx, `INSERT INTO live_events(live_session_id,direction,event_type,event_id,dedupe_key,payload)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT(live_session_id,dedupe_key) DO NOTHING RETURNING true`,
		sessionID, direction, typ, eventID(event), dedupe, payload).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	// A recover/close operation can replace or finish a session while its
	// reader is unwinding. Keep that late event for diagnostics, but never let
	// it revive the old session or append stale transcript text.
	if isTerminalLiveSessionStatus(currentStatus) {
		return tx.Commit()
	}
	mutation, err := m.projectEventTx(ctx, tx, sessionID, typ, event)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.applyTranscriptMutation(mutation)
	return nil
}

func (m *liveManager) projectEventTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, typ string, event map[string]any) (transcriptMutation, error) {
	mutation := transcriptMutation{append: make(map[string]string)}
	if typ == "session.started" {
		if err := m.eventProjectUpdate(ctx, tx, sessionID, "active"); err != nil {
			return mutation, err
		}
		_, err := tx.ExecContext(ctx, `UPDATE live_conversations SET status='active',updated_at=now()
			WHERE active_session_id=$1 AND EXISTS(SELECT 1 FROM live_sessions WHERE id=$1 AND status='active')`, sessionID)
		return mutation, err
	}
	if typ == "session.closed" {
		if err := m.flushTranscriptsTx(ctx, tx, sessionID, &mutation); err != nil {
			return mutation, err
		}
		if err := m.eventProjectUpdate(ctx, tx, sessionID, "closed"); err != nil {
			return mutation, err
		}
		_, err := tx.ExecContext(ctx, `UPDATE live_conversations SET active_session_id=NULL,status='active',updated_at=now()
			WHERE active_session_id=$1`, sessionID)
		return mutation, err
	}
	if typ == "error" {
		_, err := tx.ExecContext(ctx, `UPDATE live_sessions SET last_error=NULLIF($2,''),updated_at=now() WHERE id=$1`, sessionID, eventText(event))
		return mutation, err
	}
	if role, phase, ok := transcriptEvent(typ); ok {
		key := transcriptKey(sessionID, role, typ, event)
		switch phase {
		case "delta":
			if text := eventDelta(event); text != "" {
				mutation.append[key] = text
			}
		case "added", "done":
			text := transcriptCompletionText(phase, m.transcriptValue(key), event)
			if text != "" {
				// Delta, added, done and turn.done for one item share this
				// logical source key. The physical event id is already used by
				// live_events for frame-level de-duplication.
				if _, err := m.appendLiveMessageTx(ctx, tx, sessionID, role, text, "transcript:"+key); err != nil {
					return mutation, err
				}
			}
			mutation.clear = append(mutation.clear, key)
		}
		return mutation, nil
	}
	if typ == "turn.done" {
		if err := m.flushTranscriptsTx(ctx, tx, sessionID, &mutation); err != nil {
			return mutation, err
		}
	}
	return mutation, nil
}

func (m *liveManager) transcriptValue(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transcripts[key]
}

func (m *liveManager) applyTranscriptMutation(mutation transcriptMutation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, text := range mutation.append {
		m.transcripts[key] += text
	}
	for _, key := range mutation.clear {
		delete(m.transcripts, key)
	}
}

func (m *liveManager) flushTranscriptsTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, mutation *transcriptMutation) error {
	prefix := sessionID.String() + ":"
	m.mu.Lock()
	pending := make(map[string]string)
	for key, text := range m.transcripts {
		if strings.HasPrefix(key, prefix) && strings.TrimSpace(text) != "" {
			pending[key] = text
		}
	}
	m.mu.Unlock()
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		text := pending[key]
		parts := strings.SplitN(strings.TrimPrefix(key, prefix), ":", 2)
		if len(parts) != 2 {
			continue
		}
		if _, err := m.appendLiveMessageTx(ctx, tx, sessionID, parts[0], text, "transcript:"+key); err != nil {
			return err
		}
		mutation.clear = append(mutation.clear, key)
	}
	return nil
}

func (m *liveManager) appendLiveMessageTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID, role, text, sourceEventID string) (bool, error) {
	if role != "developer" && role != "user" && role != "assistant" {
		return false, fmt.Errorf("不支持的 Live message role %q", role)
	}
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	var conversationID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM live_sessions WHERE id=$1 FOR SHARE`, sessionID).Scan(&conversationID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, conversationID.String()); err != nil {
		return false, err
	}
	if sourceEventID != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM live_messages WHERE source_session_id=$1 AND source_event_id=$2)`, sessionID, sourceEventID).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO live_messages(conversation_id,sequence,role,text,source_session_id,source_event_id)
		SELECT $1,COALESCE(MAX(sequence),0)+1,$2,$3,$4,$5 FROM live_messages
		WHERE conversation_id=$1`, conversationID, role, text, sessionID, sourceEventID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE live_conversations SET context_revision=context_revision+1,updated_at=now() WHERE id=$1`, conversationID); err != nil {
		return false, err
	}
	return true, nil
}

func (m *liveManager) restoreTranscripts(ctx context.Context, sessionID uuid.UUID) error {
	rows, err := m.db.QueryContext(ctx, `SELECT event_type,payload FROM live_events WHERE live_session_id=$1 ORDER BY id`, sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	m.mu.Lock()
	for key := range m.transcripts {
		if strings.HasPrefix(key, sessionID.String()+":") {
			delete(m.transcripts, key)
		}
	}
	m.mu.Unlock()
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			return err
		}
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		role, phase, ok := transcriptEvent(typ)
		if ok {
			key := transcriptKey(sessionID, role, typ, event)
			if phase == "delta" {
				m.mu.Lock()
				m.transcripts[key] += eventDelta(event)
				m.mu.Unlock()
			} else {
				m.mu.Lock()
				delete(m.transcripts, key)
				m.mu.Unlock()
			}
		}
		if typ == "turn.done" || typ == "session.closed" {
			m.mu.Lock()
			for key := range m.transcripts {
				if strings.HasPrefix(key, sessionID.String()+":") {
					delete(m.transcripts, key)
				}
			}
			m.mu.Unlock()
		}
	}
	return rows.Err()
}

func transcriptCompletionText(phase, accumulated string, event map[string]any) string {
	if phase == "done" && strings.TrimSpace(accumulated) != "" {
		return accumulated
	}
	return eventText(event)
}

func transcriptEvent(typ string) (role, phase string, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(typ))
	if isAudioEvent(lower) && !strings.Contains(lower, "transcript") {
		return "", "", false
	}
	switch {
	case strings.Contains(lower, "input_transcript"), strings.Contains(lower, "input_audio_transcription"):
		role = "user"
	case strings.Contains(lower, "output_transcript"), strings.Contains(lower, "output_audio_transcript"), strings.Contains(lower, "output_text"):
		role = "assistant"
	default:
		return "", "", false
	}
	switch {
	case strings.HasSuffix(lower, ".delta"):
		return role, "delta", true
	case strings.HasSuffix(lower, ".added"):
		return role, "added", true
	case strings.HasSuffix(lower, ".done"), strings.HasSuffix(lower, ".completed"):
		return role, "done", true
	default:
		return "", "", false
	}
}

func transcriptKey(sessionID uuid.UUID, roleOrType string, args ...any) string {
	role := roleOrType
	typ := roleOrType
	var event map[string]any
	if len(args) == 1 {
		if value, ok := args[0].(map[string]any); ok {
			event = value
			role, _, _ = transcriptEvent(typ)
		}
	} else if len(args) == 2 {
		if value, ok := args[0].(string); ok {
			role = roleOrType
			typ = value
		}
		if value, ok := args[1].(map[string]any); ok {
			event = value
		}
	}
	if event == nil {
		event = map[string]any{}
	}
	identity := ""
	identity = transcriptIdentity(event)
	if identity == "" {
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(typ), ".delta"), ".done"), ".completed")
		base = strings.TrimSuffix(base, ".added")
		identity = base
	}
	return sessionID.String() + ":" + role + ":" + identity
}

func transcriptIdentity(event map[string]any) string {
	for _, name := range []string{"item_id", "itemId", "response_id", "responseId"} {
		if value, ok := event[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, container := range []string{"item", "response", "turn"} {
		if object, ok := event[container].(map[string]any); ok {
			if value := nestedTranscriptIdentity(object); value != "" {
				return value
			}
		}
	}
	return ""
}

func nestedTranscriptIdentity(value any) string {
	switch item := value.(type) {
	case map[string]any:
		for _, name := range []string{"item_id", "itemId", "response_id", "responseId", "id"} {
			if candidate, ok := item[name].(string); ok && strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
		for _, child := range item {
			if candidate := nestedTranscriptIdentity(child); candidate != "" {
				return candidate
			}
		}
	case []any:
		for _, child := range item {
			if candidate := nestedTranscriptIdentity(child); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func eventDelta(event map[string]any) string {
	return nestedTranscriptField(event, "delta")
}

func nestedTranscriptField(value any, field string) string {
	switch item := value.(type) {
	case map[string]any:
		if candidate, ok := item[field].(string); ok && strings.TrimSpace(candidate) != "" {
			return candidate
		}
		for _, child := range item {
			if candidate := nestedTranscriptField(child, field); candidate != "" {
				return candidate
			}
		}
	case []any:
		for _, child := range item {
			if candidate := nestedTranscriptField(child, field); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

func isAudioEvent(typ string) bool {
	lower := strings.ToLower(typ)
	if strings.Contains(lower, "transcript") || strings.Contains(lower, "transcription") {
		return false
	}
	return strings.Contains(lower, "audio") || strings.Contains(lower, "pcm")
}

func sanitizeLivePayload(value any, audio bool) any {
	return sanitizeLivePayloadAt(value, audio, "")
}

func sanitizeLivePayloadAt(value any, audio bool, parentKey string) any {
	if audio {
		result := map[string]any{}
		if object, ok := value.(map[string]any); ok {
			if typ, ok := object["type"].(string); ok {
				result["type"] = typ
			}
			if id := eventID(object); id != "" {
				result["eventId"] = id
			}
			for key, child := range object {
				if !isAudioPayloadKey(key) && key != "delta" {
					continue
				}
				if raw, ok := child.(string); ok {
					result["audioBytes"] = len(raw) * 3 / 4
					break
				}
			}
		}
		return result
	}
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, child := range item {
			normalizedKey := normalizedLivePayloadKey(key)
			if isSensitiveLivePayloadKey(key) ||
				(normalizedKey == "id" && (normalizedLivePayloadKey(parentKey) == "session" || parentKey == "")) {
				continue
			}
			result[key] = sanitizeLivePayloadAt(child, false, key)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = sanitizeLivePayloadAt(child, false, parentKey)
		}
		return result
	default:
		return value
	}
}

func normalizedLivePayloadKey(key string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
}

func isAudioPayloadKey(key string) bool {
	switch normalizedLivePayloadKey(key) {
	case "audio", "audiobase64", "base64", "pcm", "audiodata":
		return true
	default:
		return false
	}
}

func isSensitiveLivePayloadKey(key string) bool {
	normalized := normalizedLivePayloadKey(key)
	if isAudioPayloadKey(normalized) {
		return true
	}
	switch normalized {
	case "authorization", "apikey", "baseurl", "remotesessionid", "sessionid", "url":
		return true
	default:
		return false
	}
}

func eventText(event map[string]any) string {
	for _, key := range []string{"text", "transcript", "delta", "message"} {
		if value := nestedTranscriptField(event, key); value != "" {
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

func (m *liveManager) logWarn(message string, sessionID uuid.UUID, err error) {
	if m.logger != nil {
		m.logger.Warn(message, zap.String("sessionId", sessionID.String()), zap.Error(err))
	}
}
