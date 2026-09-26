-- 权限审批及 MCP 交互保留原生提案与表单，复用已有原生请求身份。
ALTER TABLE codex_interactive_requests DROP CONSTRAINT interactive_request_method_check;
ALTER TABLE codex_interactive_requests ADD CONSTRAINT interactive_request_method_check
    CHECK(request_method IN ('item/tool/requestUserInput',
        'item/commandExecution/requestApproval','item/fileChange/requestApproval',
        'item/permissions/requestApproval','mcpServer/elicitation/request'));
