ALTER TABLE admin_sessions
    ADD COLUMN csrf_token_ciphertext bytea;
