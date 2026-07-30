ALTER TABLE codex_turn_intents
    DROP CONSTRAINT codex_turn_intents_operation_check,
    ADD CONSTRAINT codex_turn_intents_operation_check
        CHECK (operation IN ('turn_input', 'interrupt', 'replace_last_turn')),
    DROP CONSTRAINT codex_turn_intents_resolved_action_check,
    ADD CONSTRAINT codex_turn_intents_resolved_action_check
        CHECK (resolved_action IN ('start', 'steer', 'start_after_active', 'interrupt', 'replace')),
    ADD COLUMN projection_anchor text,
    ADD COLUMN message_edit_revision bigint NOT NULL DEFAULT 0
        CHECK (message_edit_revision >= 0),
    ADD COLUMN replacement_phase text
        CHECK (replacement_phase IN ('reserved', 'interrupting', 'rollback_pending',
            'rollback_applied', 'start_pending', 'running', 'terminal')),
    ADD COLUMN replacement_error text;

UPDATE codex_turn_intents
SET projection_anchor = CASE
    WHEN input_surface = 'discord' AND COALESCE(discord_message_id, '') <> ''
        THEN discord_message_id
    ELSE 'desktop-' || id::text
END
WHERE projection_anchor IS NULL;

ALTER TABLE discord_input_messages
    ADD COLUMN edited_at timestamptz,
    ADD COLUMN edit_revision bigint NOT NULL DEFAULT 0 CHECK (edit_revision >= 0),
    ADD COLUMN replacement_previous_intent_id uuid
        REFERENCES codex_turn_intents(id) ON DELETE SET NULL;

CREATE INDEX codex_turn_intents_latest_submitted_input
    ON codex_turn_intents(control_id, sequence_no DESC)
    WHERE operation IN ('turn_input', 'replace_last_turn');

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 16;
UPDATE execution_nodes
SET protocol_version = 16, status = 'pending', last_error = NULL, updated_at = now();

CREATE OR REPLACE FUNCTION reconcile_conversation_running_tag()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    conversation_id uuid := NEW.discord_conversation_id;
    thread_id text;
    active boolean;
    tag_operation_key text;
    desired_payload jsonb;
BEGIN
    IF conversation_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT conversation.thread_id INTO thread_id
    FROM discord_conversations conversation WHERE conversation.id = conversation_id;
    IF thread_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT EXISTS(
        SELECT 1 FROM codex_turn_intents intent
        WHERE intent.discord_conversation_id = conversation_id
          AND (intent.status IN ('placement_pending','queued','dispatching',
                'awaiting_confirmation','running','reconciling','retry_wait')
            OR (intent.operation = 'replace_last_turn'
                AND COALESCE(intent.replacement_phase, 'reserved') <> 'terminal'))
    ) INTO active;
    tag_operation_key := 'conversation-running-tag:' || conversation_id::text;
    desired_payload := jsonb_build_object('channelId', thread_id,
        'tagName', 'Running', 'enabled', active);
    INSERT INTO integration_outbox(integration, operation_key, operation_type,
        route_key, payload)
    VALUES ('discord', tag_operation_key, 'thread.tag.toggle',
        'channels/' || thread_id || '/tags/Running', desired_payload)
    ON CONFLICT(integration, operation_key) DO UPDATE SET
        operation_type = EXCLUDED.operation_type,
        route_key = EXCLUDED.route_key,
        payload = EXCLUDED.payload,
        request_revision = integration_outbox.request_revision + 1,
        status = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.status ELSE 'pending' END,
        attempt_count = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.attempt_count ELSE 0 END,
        apply_attempt_count = CASE WHEN integration_outbox.status IN ('sending','applying','ambiguous')
            THEN integration_outbox.apply_attempt_count ELSE 0 END,
        available_at = now(),
        last_error = NULL,
        updated_at = now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER codex_turn_intents_running_tag
AFTER INSERT OR UPDATE OF status, replacement_phase ON codex_turn_intents
FOR EACH ROW EXECUTE FUNCTION reconcile_conversation_running_tag();
