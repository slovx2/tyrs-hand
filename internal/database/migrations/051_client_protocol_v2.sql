CREATE TABLE control_instances (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton boolean NOT NULL DEFAULT true CHECK (singleton),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(singleton)
);

INSERT INTO control_instances(singleton) VALUES (true)
ON CONFLICT(singleton) DO NOTHING;

ALTER TABLE development_sessions
    ADD COLUMN title_revision bigint NOT NULL DEFAULT 0 CHECK (title_revision >= 0),
    ADD COLUMN title_source text NOT NULL DEFAULT 'fallback'
        CHECK (title_source IN ('fallback','generating','generated','manual')),
    ADD COLUMN generated_title text;

ALTER TABLE session_messages
    ADD COLUMN turn_intent_id uuid REFERENCES codex_turn_intents(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX session_messages_turn_intent
    ON session_messages(turn_intent_id) WHERE turn_intent_id IS NOT NULL;

CREATE TABLE client_user_preferences (
    administrator_id uuid PRIMARY KEY REFERENCES administrators(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    model text,
    reasoning_effort text
        CHECK (reasoning_effort IS NULL OR reasoning_effort IN
            ('low','medium','high','xhigh','max','ultra')),
    service_tier text NOT NULL DEFAULT 'standard'
        CHECK (service_tier IN ('standard','fast')),
    collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE session_attachments (
    id uuid PRIMARY KEY,
    session_id uuid REFERENCES development_sessions(id) ON DELETE CASCADE,
    uploaded_by_device_id uuid REFERENCES client_devices(id) ON DELETE SET NULL,
    source_type text NOT NULL CHECK (source_type IN ('client','discord','agent')),
    source_key text,
    kind text NOT NULL CHECK (kind IN ('image','file')),
    original_filename text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 26214400),
    sha256 char(64) NOT NULL,
    storage_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'uploaded'
        CHECK (status IN ('uploaded','attached','deleted')),
    created_at timestamptz NOT NULL DEFAULT now(),
    attached_at timestamptz,
    UNIQUE(source_type, source_key)
);

CREATE INDEX session_attachments_orphans
    ON session_attachments(created_at) WHERE status='uploaded';

CREATE TABLE session_message_attachments (
    message_id uuid NOT NULL REFERENCES session_messages(id) ON DELETE CASCADE,
    attachment_id uuid NOT NULL REFERENCES session_attachments(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY(message_id, attachment_id),
    UNIQUE(message_id, ordinal)
);

INSERT INTO session_attachments(id,session_id,source_type,source_key,kind,
    original_filename,media_type,size_bytes,sha256,storage_key,status,attached_at,created_at)
SELECT attachment.id,conversation.session_id,'discord',attachment.discord_attachment_id,
    attachment.kind,attachment.original_filename,attachment.media_type,attachment.size_bytes,
    attachment.sha256,attachment.storage_key,'attached',attachment.stored_at,attachment.created_at
FROM discord_attachments attachment
JOIN discord_input_messages message ON message.message_id=attachment.message_id
JOIN discord_conversations conversation ON conversation.id=message.conversation_id
WHERE conversation.session_id IS NOT NULL AND attachment.status='ready'
  AND attachment.storage_key IS NOT NULL AND attachment.sha256 IS NOT NULL
ON CONFLICT(source_type,source_key) DO NOTHING;

INSERT INTO session_message_attachments(message_id,attachment_id,ordinal)
SELECT session_message.id,generic.id,
    row_number() OVER (PARTITION BY session_message.id
        ORDER BY attachment.created_at,attachment.id)-1
FROM discord_attachments attachment
JOIN discord_input_messages input ON input.message_id=attachment.message_id
JOIN session_messages session_message ON session_message.turn_intent_id=input.turn_intent_id
JOIN session_attachments generic ON generic.source_type='discord'
    AND generic.source_key=attachment.discord_attachment_id
ON CONFLICT(message_id,attachment_id) DO NOTHING;

CREATE TABLE client_push_tokens (
    device_id uuid NOT NULL REFERENCES client_devices(id) ON DELETE CASCADE,
    expo_push_token text NOT NULL,
    platform text NOT NULL CHECK (platform IN ('ios','android')),
    app_environment text NOT NULL CHECK (app_environment IN ('development','preview','production')),
    enabled boolean NOT NULL DEFAULT true,
    last_registered_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(device_id, expo_push_token)
);

CREATE UNIQUE INDEX client_push_tokens_token
    ON client_push_tokens(expo_push_token);

CREATE TABLE client_notification_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES development_sessions(id) ON DELETE CASCADE,
    notification_type text NOT NULL
        CHECK (notification_type IN ('run.completed','run.failed','interactive.required')),
    idempotency_key text NOT NULL UNIQUE,
    title text NOT NULL,
    body text NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','sending','retrying','delivered','failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz
);

CREATE INDEX client_notification_outbox_dispatch
    ON client_notification_outbox(status, available_at, created_at)
    WHERE status IN ('pending','retrying');

ALTER TABLE client_updates
    ADD COLUMN entity_type text,
    ADD COLUMN entity_version bigint,
    ADD COLUMN durable boolean NOT NULL DEFAULT true;

CREATE INDEX client_updates_created_cursor
    ON client_updates(created_at, cursor);

ALTER TABLE codex_thread_lifecycle_requests
    DROP CONSTRAINT codex_thread_lifecycle_requests_source_check,
    ADD CONSTRAINT codex_thread_lifecycle_requests_source_check
        CHECK (source IN ('desktop','discord','client')),
    ADD COLUMN requested_by_administrator_id uuid
        REFERENCES administrators(id) ON DELETE SET NULL;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 20;
