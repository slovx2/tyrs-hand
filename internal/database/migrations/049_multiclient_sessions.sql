CREATE TABLE development_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    development_environment_id uuid NOT NULL
        REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    development_project_id uuid NOT NULL
        REFERENCES development_projects(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    created_by_administrator_id uuid REFERENCES administrators(id) ON DELETE SET NULL,
    title text NOT NULL DEFAULT '',
    lifecycle_state text NOT NULL DEFAULT 'active'
        CHECK (lifecycle_state IN ('active','archive_pending','archived','unarchive_pending')),
    history_completeness text NOT NULL DEFAULT 'complete'
        CHECK (history_completeness IN ('complete','partial')),
    model text,
	reasoning_effort text
		CHECK (reasoning_effort IS NULL OR reasoning_effort IN
			('low','medium','high','xhigh','max','ultra')),
    service_tier text NOT NULL DEFAULT 'standard'
        CHECK (service_tier IN ('standard','fast')),
    collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan')),
    settings_version bigint NOT NULL DEFAULT 0 CHECK (settings_version >= 0),
    last_message_seq bigint NOT NULL DEFAULT 0 CHECK (last_message_seq >= 0),
    last_activity_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX development_sessions_activity
    ON development_sessions(lifecycle_state, last_activity_at DESC, id DESC);
CREATE INDEX development_sessions_environment
    ON development_sessions(development_environment_id, last_activity_at DESC);

ALTER TABLE codex_thread_controls ADD COLUMN session_id uuid;
ALTER TABLE codex_turn_intents ADD COLUMN session_id uuid;
ALTER TABLE codex_interactive_requests ADD COLUMN session_id uuid;
ALTER TABLE discord_conversations ADD COLUMN session_id uuid;

ALTER TABLE codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_session_fk
        FOREIGN KEY(session_id) REFERENCES development_sessions(id) ON DELETE CASCADE;
ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_session_fk
        FOREIGN KEY(session_id) REFERENCES development_sessions(id) ON DELETE CASCADE;
ALTER TABLE codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_session_fk
        FOREIGN KEY(session_id) REFERENCES development_sessions(id) ON DELETE CASCADE;
ALTER TABLE discord_conversations
    ADD CONSTRAINT discord_conversations_session_fk
        FOREIGN KEY(session_id) REFERENCES development_sessions(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX codex_controls_session_scope
    ON codex_thread_controls(session_id) WHERE session_id IS NOT NULL;
CREATE UNIQUE INDEX discord_conversations_session_scope
    ON discord_conversations(session_id) WHERE session_id IS NOT NULL;
CREATE INDEX codex_intents_session ON codex_turn_intents(session_id, sequence_no);
CREATE INDEX codex_interactive_requests_session
    ON codex_interactive_requests(session_id, created_at DESC);

CREATE TABLE session_surface_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL REFERENCES development_sessions(id) ON DELETE CASCADE,
    surface_type text NOT NULL,
    external_key text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(surface_type, external_key),
    UNIQUE(session_id, surface_type)
);

CREATE TABLE participants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('administrator','discord')),
    display_name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE participant_identities (
    participant_id uuid NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
    provider text NOT NULL,
    external_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(provider, external_key),
    UNIQUE(participant_id, provider)
);

CREATE TABLE session_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL REFERENCES development_sessions(id) ON DELETE CASCADE,
    seq bigint NOT NULL CHECK (seq > 0),
    local_id text NOT NULL,
    participant_id uuid REFERENCES participants(id) ON DELETE SET NULL,
    message_role text NOT NULL CHECK (message_role IN ('user','agent','event')),
    content jsonb NOT NULL,
    source_event_id bigint REFERENCES agent_events(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(session_id, seq),
    UNIQUE(session_id, local_id)
);
CREATE INDEX session_messages_window ON session_messages(session_id, seq DESC);

CREATE TABLE client_updates (
    cursor bigserial PRIMARY KEY,
    session_id uuid REFERENCES development_sessions(id) ON DELETE CASCADE,
    update_type text NOT NULL,
    entity_id text NOT NULL,
    entity_seq bigint,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX client_updates_session ON client_updates(session_id, cursor);
CREATE INDEX client_updates_retention ON client_updates(created_at);

ALTER TABLE codex_thread_controls
    DROP CONSTRAINT codex_thread_controls_source_check,
    DROP CONSTRAINT codex_thread_controls_source_type_check;
ALTER TABLE codex_turn_intents
    DROP CONSTRAINT codex_turn_intents_source_check,
    DROP CONSTRAINT codex_turn_intents_source_type_check;
ALTER TABLE codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_source_type_check
        CHECK (source_type IN ('github_work_item','development_session')) NOT VALID,
    ADD CONSTRAINT codex_thread_controls_source_check CHECK (
        (source_type='github_work_item' AND work_item_id IS NOT NULL AND session_id IS NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL
            AND development_project_id IS NULL)
        OR
        (source_type='development_session' AND work_item_id IS NULL AND session_id IS NOT NULL
            AND development_environment_id IS NOT NULL AND repository_id IS NULL
            AND development_project_id IS NOT NULL)
    ) NOT VALID;
ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_source_type_check
        CHECK (source_type IN ('github_work_item','development_session')) NOT VALID,
    ADD CONSTRAINT codex_turn_intents_source_check CHECK (
        (source_type='github_work_item' AND work_item_id IS NOT NULL AND session_id IS NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL
            AND development_project_id IS NULL)
        OR
        (source_type='development_session' AND work_item_id IS NULL AND session_id IS NOT NULL
            AND repository_id IS NULL AND development_project_id IS NOT NULL)
    ) NOT VALID;

ALTER TABLE codex_turn_intents DROP CONSTRAINT codex_turn_intents_input_surface_check;
ALTER TABLE codex_turn_intents ADD CONSTRAINT codex_turn_intents_input_surface_check
    CHECK (input_surface IN ('discord','desktop','client'));
ALTER TABLE codex_interactive_requests
    DROP CONSTRAINT codex_interactive_requests_answer_surface_check;
ALTER TABLE codex_interactive_requests
    ADD CONSTRAINT codex_interactive_requests_answer_surface_check
        CHECK (answer_surface IN ('desktop','discord','client','auto'));

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 19;
