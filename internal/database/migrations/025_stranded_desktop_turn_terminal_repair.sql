CREATE TEMP TABLE stranded_desktop_turn_terminal_repair ON COMMIT DROP AS
SELECT
    intent.id AS intent_id,
    control.id AS control_id,
    (
        intent.last_error_code = 'user_interrupt'
        OR lower(COALESCE(intent.last_error_message, ''))
            ~ '(interrupted|cancelled|canceled)'
    ) AS canceled
FROM codex_turn_intents AS intent
JOIN codex_thread_controls AS control ON control.id = intent.control_id
WHERE intent.input_surface = 'desktop'
  AND intent.status IN ('retry_wait', 'reconciling')
  AND control.status = 'reconciling'
  AND (control.active_intent_id IS NULL OR control.active_intent_id = intent.id);

UPDATE codex_turn_intents AS intent
SET
    status = CASE WHEN repair.canceled THEN 'canceled' ELSE 'failed' END,
    last_error_code = CASE
        WHEN repair.canceled THEN 'user_interrupt'
        ELSE intent.last_error_code
    END,
    available_at = now(),
    finished_at = COALESCE(intent.finished_at, now()),
    updated_at = now()
FROM stranded_desktop_turn_terminal_repair AS repair
WHERE intent.id = repair.intent_id;

UPDATE codex_turn_runs AS run
SET
    status = CASE WHEN repair.canceled THEN 'canceled' ELSE 'failed' END,
    active_slot = NULL,
    error_code = CASE
        WHEN repair.canceled THEN 'user_interrupt'
        ELSE run.error_code
    END,
    finished_at = COALESCE(run.finished_at, now())
FROM stranded_desktop_turn_terminal_repair AS repair
WHERE run.primary_intent_id = repair.intent_id
  AND run.attempt = (
      SELECT max(latest.attempt)
      FROM codex_turn_runs AS latest
      WHERE latest.primary_intent_id = repair.intent_id
  );

UPDATE codex_thread_controls AS control
SET
    status = 'idle',
    active_intent_id = NULL,
    remote_status = 'idle',
    active_codex_turn_id = NULL,
    active_client_id = NULL,
    worker_id = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    next_wakeup_at = NULL,
    last_error_code = CASE
        WHEN EXISTS (
            SELECT 1
            FROM stranded_desktop_turn_terminal_repair AS repair
            WHERE repair.control_id = control.id AND repair.canceled
        ) THEN 'user_interrupt'
        ELSE control.last_error_code
    END,
    updated_at = now()
WHERE EXISTS (
    SELECT 1
    FROM stranded_desktop_turn_terminal_repair AS repair
    WHERE repair.control_id = control.id
)
AND (
    control.active_intent_id IS NULL
    OR EXISTS (
        SELECT 1
        FROM stranded_desktop_turn_terminal_repair AS repair
        WHERE repair.control_id = control.id
          AND repair.intent_id = control.active_intent_id
    )
);
