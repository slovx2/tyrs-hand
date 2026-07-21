ALTER TABLE discord_conversations
    DROP CONSTRAINT discord_conversations_title_rename_status_check;

ALTER TABLE discord_conversations
    ADD CONSTRAINT discord_conversations_title_rename_status_check
    CHECK (title_rename_status IN ('pending','generating','scheduled','completed','failed','skipped'));

CREATE INDEX discord_conversations_pending_title
    ON discord_conversations(created_at, id)
    WHERE title_rename_status = 'pending';
