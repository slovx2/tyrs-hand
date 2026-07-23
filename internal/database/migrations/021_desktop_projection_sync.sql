ALTER TABLE codex_thread_controls
    DROP CONSTRAINT IF EXISTS codex_thread_controls_source_type_check,
    DROP CONSTRAINT IF EXISTS codex_thread_controls_check,
    ADD CONSTRAINT codex_thread_controls_source_type_check
        CHECK (source_type IN ('github_work_item', 'discord_conversation', 'desktop_thread')),
    ADD CONSTRAINT codex_thread_controls_source_check CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND discord_conversation_id IS NOT NULL)
        OR
        (source_type = 'desktop_thread' AND work_item_id IS NULL
            AND repository_id IS NOT NULL AND development_environment_id IS NOT NULL)
    ),
    ADD COLUMN desired_thread_name text,
    ADD COLUMN desired_thread_name_source text
        CHECK (desired_thread_name_source IS NULL
            OR desired_thread_name_source IN ('preview','codex','luna')),
    ADD COLUMN desired_thread_name_revision bigint NOT NULL DEFAULT 0
        CHECK (desired_thread_name_revision >= 0),
    ADD COLUMN applied_thread_name text,
    ADD COLUMN applied_thread_name_revision bigint NOT NULL DEFAULT 0
        CHECK (applied_thread_name_revision >= 0),
    ADD COLUMN thread_name_last_error text,
    ADD COLUMN app_server_event_generation bigint NOT NULL DEFAULT 0
        CHECK (app_server_event_generation >= 0),
    ADD COLUMN app_server_event_sequence bigint NOT NULL DEFAULT 0
        CHECK (app_server_event_sequence >= 0);

ALTER TABLE desktop_thread_requests
    DROP CONSTRAINT desktop_thread_requests_status_check,
    ADD CONSTRAINT desktop_thread_requests_status_check CHECK (status IN (
        'preparing','thread_bound','waiting_for_input','post_pending',
        'completed','post_failed','failed'
    )),
    ADD COLUMN first_input_projection_key text,
    ADD COLUMN first_input jsonb,
    ADD COLUMN first_input_text text,
    ADD COLUMN preview_title text,
    ADD COLUMN first_input_actor_discord_user_id text,
    ADD COLUMN first_input_actor_display_name text,
    ADD COLUMN codex_user_message_item_id text;

ALTER TABLE codex_turn_intents
    DROP CONSTRAINT IF EXISTS codex_turn_intents_check,
    ADD CONSTRAINT codex_turn_intents_source_check CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND (discord_conversation_id IS NOT NULL OR input_surface = 'desktop'))
    ),
    ADD COLUMN desktop_input_projection_key text,
    ADD COLUMN codex_user_message_item_id text,
    ADD COLUMN desktop_input_projection_status text NOT NULL DEFAULT 'not_applicable'
        CHECK (desktop_input_projection_status IN (
            'not_applicable','pending','projected','failed'
        ));

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 4;
UPDATE execution_nodes SET protocol_version = 4, status = 'pending', last_error = NULL;
