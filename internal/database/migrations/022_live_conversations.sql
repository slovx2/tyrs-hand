CREATE TABLE IF NOT EXISTS live_conversations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
 model text NOT NULL, voice text NOT NULL DEFAULT '', instructions text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'active',
 active_session_id uuid, context_revision bigint NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS live_sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), conversation_id uuid NOT NULL REFERENCES live_conversations(id) ON DELETE CASCADE,
 remote_session_id text NOT NULL, client_platform text NOT NULL, status text NOT NULL DEFAULT 'creating', started_at timestamptz,
 disconnected_at timestamptz, expired_at timestamptz, closed_at timestamptz, last_error text,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS live_messages (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), conversation_id uuid NOT NULL REFERENCES live_conversations(id) ON DELETE CASCADE,
 sequence bigint NOT NULL, role text NOT NULL, text text NOT NULL, source_session_id uuid REFERENCES live_sessions(id) ON DELETE SET NULL,
 source_event_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(conversation_id, sequence)
);
CREATE TABLE IF NOT EXISTS live_events (
 id bigserial PRIMARY KEY, live_session_id uuid NOT NULL REFERENCES live_sessions(id) ON DELETE CASCADE,
 direction text NOT NULL, event_type text NOT NULL, event_id text NOT NULL DEFAULT '', dedupe_key text NOT NULL,
 payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(live_session_id, dedupe_key)
);
CREATE INDEX IF NOT EXISTS live_conversations_administrator_idx ON live_conversations(administrator_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS live_messages_conversation_idx ON live_messages(conversation_id, sequence DESC);
CREATE INDEX IF NOT EXISTS live_events_session_idx ON live_events(live_session_id, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS live_one_active_session_idx ON live_sessions(conversation_id)
 WHERE status IN ('creating','active','sideband_disconnected','recovering');
ALTER TABLE live_conversations ADD CONSTRAINT live_conversations_active_session_fk
 FOREIGN KEY (active_session_id) REFERENCES live_sessions(id) ON DELETE SET NULL;
