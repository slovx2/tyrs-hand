ALTER TABLE job_intents
    ADD COLUMN actor_login text NOT NULL DEFAULT '',
    ADD COLUMN actor_permission text NOT NULL DEFAULT '';
