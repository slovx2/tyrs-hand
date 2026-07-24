ALTER TABLE codex_auth_operations
    RENAME COLUMN auth_url TO verification_url;

ALTER TABLE codex_auth_operations
    ADD COLUMN user_code text;
