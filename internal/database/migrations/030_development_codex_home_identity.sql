UPDATE codex_thread_controls
SET codex_home_key = development_environment_id::text,
    updated_at = now()
WHERE development_environment_id IS NOT NULL
    AND codex_home_key IS DISTINCT FROM development_environment_id::text;
