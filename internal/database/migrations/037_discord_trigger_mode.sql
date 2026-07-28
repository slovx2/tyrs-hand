ALTER TABLE discord_conversations
    ADD COLUMN trigger_mode text NOT NULL DEFAULT 'interactive'
        CHECK (trigger_mode IN ('interactive','discussion')),
    ADD COLUMN trigger_mode_revision bigint NOT NULL DEFAULT 0
        CHECK (trigger_mode_revision >= 0);

ALTER TABLE discord_input_messages
    ADD COLUMN turn_intent_id uuid
        REFERENCES codex_turn_intents(id) ON DELETE SET NULL;

UPDATE discord_input_messages message
SET turn_intent_id = intent.id
FROM codex_turn_intents intent
WHERE intent.discord_message_id = message.message_id
    AND message.turn_intent_id IS NULL;

CREATE INDEX discord_input_messages_pending_batch
    ON discord_input_messages(conversation_id, received_at DESC, message_id DESC)
    WHERE status = 'received' AND turn_intent_id IS NULL;

CREATE INDEX discord_input_messages_turn_intent
    ON discord_input_messages(turn_intent_id, received_at, message_id)
    WHERE turn_intent_id IS NOT NULL;
