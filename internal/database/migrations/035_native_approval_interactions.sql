-- 原有问题一次性补全类型；新增审批保留原生参数以验证用户回答。
ALTER TABLE codex_interactive_requests ADD COLUMN request_method text;
ALTER TABLE codex_interactive_requests ADD COLUMN request_params jsonb;
UPDATE codex_interactive_requests SET request_method='item/tool/requestUserInput',
    request_params=jsonb_build_object('threadId',thread_id,'turnId',turn_id,
        'itemId',item_id,'questions',questions);
ALTER TABLE codex_interactive_requests ALTER COLUMN request_method SET NOT NULL;
ALTER TABLE codex_interactive_requests ALTER COLUMN request_params SET NOT NULL;
ALTER TABLE codex_interactive_requests ADD CONSTRAINT interactive_request_method_check
    CHECK(request_method IN ('item/tool/requestUserInput',
        'item/commandExecution/requestApproval','item/fileChange/requestApproval'));
-- 命令子操作可共享 itemId，回答必须进一步绑定原生回调和进程代次。
ALTER TABLE codex_interactive_requests DROP CONSTRAINT interactive_control_protocol_unique;
ALTER TABLE codex_interactive_requests ADD CONSTRAINT interactive_native_request_unique
    UNIQUE(control_id,thread_id,turn_id,item_id,app_server_generation,app_server_request_id);
CREATE UNIQUE INDEX interactive_question_item_unique ON codex_interactive_requests
    (control_id,thread_id,turn_id,item_id) WHERE request_method='item/tool/requestUserInput';
