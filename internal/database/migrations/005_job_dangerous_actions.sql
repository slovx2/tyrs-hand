ALTER TABLE job_intents
    ADD COLUMN dangerous_actions jsonb NOT NULL DEFAULT '[]'::jsonb;
