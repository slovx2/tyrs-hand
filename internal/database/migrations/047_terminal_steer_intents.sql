UPDATE codex_turn_intents AS intent
SET status = run.status,
    result = CASE WHEN run.status = 'completed' THEN intent.result ELSE NULL END,
    last_error_code = CASE WHEN run.status = 'completed' THEN NULL ELSE run.error_code END,
    last_error_message = CASE WHEN run.status = 'completed' THEN NULL ELSE run.error_message END,
    finished_at = COALESCE(intent.finished_at, run.finished_at, now()),
    result_delivery_status = CASE
        WHEN run.status = 'completed' AND intent.source_type = 'github_work_item' THEN 'skipped'
        WHEN run.status = 'completed' THEN 'delivered'
        ELSE intent.result_delivery_status
    END,
    result_delivered_at = CASE
        WHEN run.status = 'completed' THEN COALESCE(intent.result_delivered_at, run.finished_at, now())
        ELSE intent.result_delivered_at
    END,
    result_delivery_available_at = now(),
    updated_at = now()
FROM codex_turn_runs AS run
WHERE intent.control_id = run.control_id
  AND intent.id <> run.primary_intent_id
  AND intent.status = 'running'
  AND intent.resolved_action = 'steer'
  AND intent.confirmed_codex_turn_id = run.confirmed_codex_turn_id
  AND run.status IN ('completed', 'failed', 'canceled');
