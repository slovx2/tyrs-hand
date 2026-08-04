ALTER TABLE agent_events ADD COLUMN run_event_sequence bigint;
ALTER TABLE codex_turn_runs ADD COLUMN client_projection_sequence bigint NOT NULL DEFAULT 0;

WITH numbered AS (
    SELECT id, row_number() OVER (PARTITION BY run_id ORDER BY id) AS sequence
    FROM agent_events
    WHERE run_id IS NOT NULL
)
UPDATE agent_events event
SET run_event_sequence = numbered.sequence
FROM numbered
WHERE event.id = numbered.id;

CREATE UNIQUE INDEX agent_events_run_sequence
    ON agent_events(run_id, run_event_sequence)
    WHERE run_id IS NOT NULL AND run_event_sequence IS NOT NULL;

ALTER TABLE session_messages ADD COLUMN conversation_turn_id uuid;
ALTER TABLE session_messages
    ADD CONSTRAINT session_messages_conversation_turn_id_fkey
    FOREIGN KEY (conversation_turn_id) REFERENCES codex_turn_intents(id) ON DELETE SET NULL;
CREATE INDEX session_messages_conversation_turn
    ON session_messages(session_id, conversation_turn_id, seq);

UPDATE session_messages message
SET conversation_turn_id = COALESCE((
    SELECT run.primary_intent_id
    FROM codex_turn_intents intent
    JOIN codex_turn_runs run ON run.control_id = intent.control_id
      AND (run.primary_intent_id = intent.id OR
        (intent.resolved_action = 'steer' AND
         run.confirmed_codex_turn_id = intent.confirmed_codex_turn_id))
    WHERE intent.id = message.turn_intent_id
    ORDER BY run.started_at DESC
    LIMIT 1
), message.turn_intent_id)
WHERE message.turn_intent_id IS NOT NULL;

UPDATE session_messages message
SET turn_intent_id = intent.id,
    conversation_turn_id = COALESCE((
        SELECT run.primary_intent_id
        FROM codex_turn_runs run
        WHERE run.control_id = intent.control_id
          AND (run.primary_intent_id = intent.id OR
            (intent.resolved_action = 'steer' AND
             run.confirmed_codex_turn_id = intent.confirmed_codex_turn_id))
        ORDER BY run.started_at DESC
        LIMIT 1
    ), intent.id)
FROM codex_turn_intents intent
WHERE message.turn_intent_id IS NULL
  AND message.local_id = 'desktop:' || intent.idempotency_key;

UPDATE session_messages message
SET conversation_turn_id = substring(message.local_id FROM 15)::uuid
WHERE message.local_id ~ '^intent-result:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
  AND EXISTS (
    SELECT 1 FROM codex_turn_intents intent
    WHERE intent.id = substring(message.local_id FROM 15)::uuid
  );

CREATE TABLE run_process_segments (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    sequence bigint NOT NULL,
    trigger_type text NOT NULL,
    trigger_message_id uuid REFERENCES session_messages(id) ON DELETE SET NULL,
    interactive_request_id uuid REFERENCES codex_interactive_requests(id) ON DELETE SET NULL,
    boundary_client_id text,
    start_event_sequence bigint NOT NULL DEFAULT 0,
    end_event_sequence bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT run_process_segments_sequence_check CHECK (sequence >= 0),
    CONSTRAINT run_process_segments_trigger_type_check CHECK (
        trigger_type IN ('initial', 'steer', 'interactive')
    ),
    UNIQUE(run_id, sequence)
);

CREATE INDEX run_process_segments_window
    ON run_process_segments(run_id, sequence);

INSERT INTO run_process_segments(run_id, sequence, trigger_type, trigger_message_id,
    start_event_sequence)
SELECT run.id, 0, 'initial', message.id, 0
FROM codex_turn_runs run
LEFT JOIN session_messages message
  ON message.conversation_turn_id = run.primary_intent_id
 AND message.message_role = 'user'
 AND message.seq = (
    SELECT min(candidate.seq) FROM session_messages candidate
    WHERE candidate.conversation_turn_id = run.primary_intent_id
      AND candidate.message_role = 'user'
 )
ON CONFLICT(run_id, sequence) DO NOTHING;

CREATE TABLE run_process_activities (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    segment_id uuid NOT NULL REFERENCES run_process_segments(id) ON DELETE CASCADE,
    item_id text NOT NULL,
    kind text NOT NULL,
    first_event_sequence bigint NOT NULL,
    last_event_sequence bigint NOT NULL,
    status text NOT NULL DEFAULT 'completed',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT run_process_activities_kind_check CHECK (
        kind IN ('commentary', 'operation', 'final_answer')
    ),
    CONSTRAINT run_process_activities_status_check CHECK (
        status IN ('running', 'completed', 'failed')
    ),
    UNIQUE(segment_id, item_id)
);

CREATE INDEX run_process_activities_window
    ON run_process_activities(segment_id, first_event_sequence DESC);
CREATE INDEX run_process_activities_event_watermark
    ON run_process_activities(run_id, last_event_sequence);
