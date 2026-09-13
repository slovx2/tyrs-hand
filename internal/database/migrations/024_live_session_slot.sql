-- close_timeout 不再占用唯一 active session 槽位。
UPDATE live_conversations
SET active_session_id=NULL, status='active', updated_at=now()
WHERE active_session_id IN (
	SELECT id FROM live_sessions WHERE status='close_timeout'
);

UPDATE live_sessions
SET status='failed',
	last_error=COALESCE(NULLIF(last_error, ''), '等待 session.closed 超时'),
	updated_at=now()
WHERE status='close_timeout';

DROP INDEX IF EXISTS live_one_active_session_idx;
CREATE UNIQUE INDEX IF NOT EXISTS live_one_active_session_idx ON live_sessions(conversation_id)
 WHERE status IN ('creating','active','sideband_disconnected','recovering');
