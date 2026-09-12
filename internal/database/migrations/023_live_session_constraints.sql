-- 修正 Live session 状态槽位和消息来源幂等约束。
DROP INDEX IF EXISTS live_one_active_session_idx;
CREATE UNIQUE INDEX IF NOT EXISTS live_one_active_session_idx ON live_sessions(conversation_id)
 WHERE status IN ('creating','active','sideband_disconnected','recovering','closing','close_timeout');
CREATE UNIQUE INDEX IF NOT EXISTS live_messages_source_event_idx ON live_messages(source_session_id, source_event_id)
 WHERE source_session_id IS NOT NULL AND source_event_id <> '';
