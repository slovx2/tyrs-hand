UPDATE discord_conversations
SET service_tier = CASE btrim(service_tier)
    WHEN '' THEN NULL
    WHEN 'default' THEN 'standard'
    WHEN 'priority' THEN 'fast'
    ELSE btrim(service_tier)
END
WHERE service_tier IS NOT NULL;

UPDATE codex_thread_controls
SET service_tier = CASE btrim(service_tier)
        WHEN '' THEN NULL
        WHEN 'default' THEN 'standard'
        WHEN 'priority' THEN 'fast'
        ELSE btrim(service_tier)
    END,
    applied_service_tier = CASE btrim(applied_service_tier)
        WHEN '' THEN NULL
        WHEN 'standard' THEN 'default'
        WHEN 'fast' THEN 'priority'
        ELSE btrim(applied_service_tier)
    END;

UPDATE codex_turn_runs
SET service_tier = CASE btrim(service_tier)
        WHEN '' THEN NULL
        WHEN 'default' THEN 'standard'
        WHEN 'priority' THEN 'fast'
        ELSE btrim(service_tier)
    END,
    applied_service_tier = CASE btrim(applied_service_tier)
        WHEN '' THEN NULL
        WHEN 'standard' THEN 'default'
        WHEN 'fast' THEN 'priority'
        ELSE btrim(applied_service_tier)
    END;

ALTER TABLE discord_conversations
    ADD CONSTRAINT discord_conversations_service_tier_check
        CHECK (service_tier IS NULL OR service_tier IN ('standard','fast'));

ALTER TABLE codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_service_tier_check
        CHECK (service_tier IS NULL OR service_tier IN ('standard','fast')),
    ADD CONSTRAINT codex_thread_controls_applied_service_tier_check
        CHECK (applied_service_tier IS NULL OR applied_service_tier IN ('default','priority'));

ALTER TABLE codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_service_tier_check
        CHECK (service_tier IS NULL OR service_tier IN ('standard','fast')),
    ADD CONSTRAINT codex_turn_runs_applied_service_tier_check
        CHECK (applied_service_tier IS NULL OR applied_service_tier IN ('default','priority'));
