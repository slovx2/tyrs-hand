// 场景只声明预期；真正请求必须来自 Claude CLI 启动的 MCP stdio 服务。
export const mobileMcpSchema = {
  type: 'object',
  properties: {
    value: { type: 'string', title: 'Name', minLength: 1 },
    count: { type: 'integer', title: 'Count', minimum: 1, maximum: 9 },
    ratio: { type: 'number', title: 'Ratio', minimum: 0, maximum: 1 },
    enabled: { type: 'boolean', title: 'Enabled' },
    color: { type: 'string', title: 'Color', enum: ['blue', 'red'] },
    note: { type: 'string', title: 'Optional note' },
  },
  required: ['value', 'count', 'ratio', 'enabled', 'color'],
}

export const mobileMcpContent = { value: 'mobile-approved', count: 3, ratio: 0, enabled: false, color: 'blue' }
export const mobileMcpTool = 'mcp__mobile_fixture__confirm_mobile'
export const mobileMcpScenarios = Object.fromEntries(['form', 'url'].flatMap((mode) =>
  ['accept', 'decline', 'cancel'].map((action) => [
    `MOBILE_CLAUDE_MCP_${mode.toUpperCase()}_${action.toUpperCase()}`, { mode, action },
  ])))

export function mobileMcpAnswer(marker) {
  const scenario = mobileMcpScenarios[marker]
  if (!scenario) throw new Error('未登记的 MCP 场景：' + marker)
  return { action: scenario.action, content: scenario.mode === 'form' && scenario.action === 'accept'
    ? structuredClone(mobileMcpContent) : null, _meta: null }
}

export const mobileScenarios = ['MOBILE_CODEX_CHAT', 'MOBILE_CLAUDE_CHAT', 'MOBILE_CLAUDE_FULL',
  'MOBILE_CLAUDE_APPROVAL', 'MOBILE_CLAUDE_DENY', 'MOBILE_CLAUDE_PLAN', ...Object.keys(mobileMcpScenarios)]
