ALTER TABLE discord_development_environments
    ADD COLUMN ssh_discord_user_id text;

UPDATE discord_development_environments
SET ssh_discord_user_id = owner_discord_user_id
WHERE ssh_public_key IS NOT NULL;

ALTER TABLE discord_development_environments
    DROP CONSTRAINT discord_development_environments_ssh_pair,
    ADD CONSTRAINT discord_development_environments_ssh_pair CHECK (
        (ssh_public_key IS NULL AND ssh_fingerprint IS NULL AND ssh_port IS NULL
            AND ssh_discord_user_id IS NULL)
        OR
        (ssh_public_key IS NOT NULL AND ssh_fingerprint IS NOT NULL AND ssh_port IS NOT NULL
            AND ssh_discord_user_id IS NOT NULL)
    ),
    ADD CONSTRAINT discord_development_environments_ssh_member
        FOREIGN KEY (guild_id, ssh_discord_user_id)
        REFERENCES discord_members(guild_id, discord_user_id);

ALTER TABLE codex_turn_intents
    ADD COLUMN actor_participant_id uuid,
    ADD COLUMN actor_display_name text NOT NULL DEFAULT '';

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 3;
UPDATE execution_nodes SET protocol_version = 3, status = 'pending', last_error = NULL;
