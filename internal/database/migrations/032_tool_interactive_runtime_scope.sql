-- Control 的全局 ID 唯一绑定 Worker 与引擎；协议提供的 thread/turn/item ID 不能作全局去重键。
ALTER TABLE codex_turn_runs ADD CONSTRAINT codex_runs_id_control_unique UNIQUE (id,control_id);
ALTER TABLE codex_turn_intents ADD CONSTRAINT codex_intents_id_control_unique UNIQUE (id,control_id);

ALTER TABLE tool_calls ADD COLUMN control_id uuid;
UPDATE tool_calls call SET control_id=run.control_id FROM codex_turn_runs run WHERE run.id=call.run_id;
ALTER TABLE tool_calls ALTER COLUMN control_id SET NOT NULL;
ALTER TABLE tool_calls
    DROP CONSTRAINT tool_calls_thread_id_turn_id_call_id_key,
    ADD CONSTRAINT tool_calls_control_protocol_unique UNIQUE (control_id,thread_id,turn_id,call_id),
    ADD CONSTRAINT tool_calls_run_control_fk FOREIGN KEY (run_id,control_id) REFERENCES codex_turn_runs(id,control_id),
    ADD CONSTRAINT tool_calls_intent_control_fk FOREIGN KEY (intent_id,control_id) REFERENCES codex_turn_intents(id,control_id);

ALTER TABLE codex_interactive_requests
    DROP CONSTRAINT codex_interactive_requests_thread_id_turn_id_item_id_key,
    ADD CONSTRAINT interactive_control_protocol_unique UNIQUE (control_id,thread_id,turn_id,item_id),
    ADD CONSTRAINT interactive_run_control_fk FOREIGN KEY (run_id,control_id) REFERENCES codex_turn_runs(id,control_id);
