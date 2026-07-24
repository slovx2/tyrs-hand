WITH ranked_controls AS (
    SELECT control.id AS control_id,
        row_number() OVER (
            PARTITION BY conversation.id
            ORDER BY CASE WHEN EXISTS (
                SELECT 1 FROM desktop_thread_requests request
                WHERE request.conversation_id = conversation.id
                    AND request.control_id = control.id
            ) THEN 0 ELSE 1 END,
            control.created_at, control.id
        ) AS priority
    FROM discord_conversations conversation
    JOIN codex_thread_controls control
        ON control.discord_conversation_id = conversation.id
)
DELETE FROM codex_thread_controls control
USING ranked_controls ranked
WHERE control.id = ranked.control_id AND ranked.priority > 1;

WITH ranked_controls AS (
    SELECT id, row_number() OVER (
        PARTITION BY work_item_id, agent_profile_id
        ORDER BY created_at DESC, id DESC
    ) AS priority
    FROM codex_thread_controls
    WHERE work_item_id IS NOT NULL
)
DELETE FROM codex_thread_controls control
USING ranked_controls ranked
WHERE control.id = ranked.id AND ranked.priority > 1;

DROP INDEX codex_controls_github_scope;
DROP INDEX codex_controls_discord_scope;

CREATE UNIQUE INDEX codex_controls_github_identity
    ON codex_thread_controls(work_item_id, agent_profile_id)
    WHERE work_item_id IS NOT NULL;

CREATE UNIQUE INDEX codex_controls_discord_identity
    ON codex_thread_controls(discord_conversation_id)
    WHERE discord_conversation_id IS NOT NULL;

ALTER TABLE agent_profiles DROP COLUMN context_version;
ALTER TABLE work_items DROP COLUMN context_version;
ALTER TABLE discord_conversations DROP COLUMN context_version;
ALTER TABLE codex_thread_controls DROP COLUMN context_version;
ALTER TABLE codex_thread_controls DROP COLUMN provider_signature;
ALTER TABLE codex_thread_controls DROP COLUMN thread_generation;
ALTER TABLE agent_profiles DROP COLUMN provider;
ALTER TABLE codex_thread_controls DROP COLUMN provider;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 8;
UPDATE execution_nodes SET protocol_version = 8, status = 'pending', last_error = NULL;
