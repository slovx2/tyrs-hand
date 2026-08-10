ALTER TABLE official_turn_submissions
    ADD COLUMN additional_context jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN developer_instructions text NOT NULL DEFAULT '';
