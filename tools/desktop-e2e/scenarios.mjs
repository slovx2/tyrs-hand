// 桌面端专属的 Mock LLM 场景。模型只按标记返回固定回复，真实 SDK、CLI、工具与 GUI 行为不被替代；
// 界面侧必须看到的内容由 gui.mjs 断言，是否真正中断、插入与换模由 serve.mjs 的 wire 断言复核。
import assert from 'node:assert/strict'

export const steerPayload = 'STEER_PAYLOAD_7F'
export const progressNote = 'DESKTOP_PROGRESS_NOTE'
export const toolOutput = 'DESKTOP_TOOL_STDOUT'
export const thinkingHeading = 'Checking desktop thinking'
const sleep = (ms) => new Promise((done) => setTimeout(done, ms))

const resultText = (found) => JSON.stringify(found?.content ?? '')

export const desktopScenarios = {
  // 工具调用与中间过程：思考、过程说明与一个持续数秒的真实命令，界面须在命令运行期间展示两者。
  DESKTOP_CLAUDE_TOOLS({ request, result, finish, tool }) {
    const found = result('toolu_desktop_tools')
    if (!found) {
      assert.ok(request.tools?.some((entry) => entry.name === 'Bash'), '真实 CLI 未声明 Bash 工具')
      return [
        { type: 'thinking', thinking: 'DESKTOP_THINKING_TRACE', signature: 'mock' },
        { type: 'text', text: progressNote },
        tool('Bash', 'toolu_desktop_tools', { command: `sleep 6; echo ${toolOutput}`, description: 'desktop tool display' }),
      ]
    }
    assert.ok(!found.is_error && resultText(found).includes(toolOutput), '真实命令输出必须回到模型')
    return finish('DESKTOP_CLAUDE_TOOLS_OK')
  },
  // 思考显示：客户端把推理摘要作为回合进行中最新的条目展示；模型持续思考期间界面须可见思考内容。
  DESKTOP_CLAUDE_THINK({ result, finish, tool }) {
    if (!result('toolu_desktop_think')) return [
      // 思考之后持续 6 秒无其他输出，思考即为回合中最新的条目。
      { type: 'thinking', thinking: `**${thinkingHeading}**\n\nDESKTOP_THINKING_BODY`, signature: 'mock', pauseAfterMs: 6_000 },
      tool('Bash', 'toolu_desktop_think', { command: 'echo THINK_WINDOW', description: 'desktop thinking display' }),
    ]
    return finish('DESKTOP_CLAUDE_THINK_OK')
  },
  // 用户回答问题（非计划模式）：须收到用户在界面选择的第二项。
  DESKTOP_CLAUDE_ASK({ result, finish, tool }) {
    const found = result('toolu_desktop_ask')
    if (!found) return [tool('AskUserQuestion', 'toolu_desktop_ask', {
      questions: [{ question: 'Pick a fruit', header: 'Fruit', multiSelect: false,
        options: [{ label: 'Apple', description: 'First option' }, { label: 'Grape', description: 'Second option' }] }],
    })]
    assert.match(resultText(found), /Grape/, '模型必须收到用户选择的 Grape')
    return finish('DESKTOP_CLAUDE_ASK_OK')
  },
  // 停止对话：模型请求挂起，由用户在界面停止；迟到的回复不能出现在界面或历史中。
  async DESKTOP_CLAUDE_STOP({ finish }) {
    finish('DESKTOP_CLAUDE_STOP_STARTED')
    await sleep(45_000)
    return [{ type: 'text', text: 'DESKTOP_STOP_TOO_LATE' }]
  },
  // steer：命令运行期间用户追加消息，工具结果之后的下一次模型请求必须包含该消息。
  DESKTOP_CLAUDE_STEER({ user, result, finish, tool }) {
    if (!result('toolu_desktop_steer')) {
      return [tool('Bash', 'toolu_desktop_steer', { command: 'sleep 20; echo STEER_WINDOW', description: 'desktop steer window' })]
    }
    assert.ok(JSON.stringify(user).includes(steerPayload), 'steer 消息必须进入同一回合的下一次模型请求')
    return finish('DESKTOP_CLAUDE_STEER_OK')
  },
  // 更换模型：界面选择 Claude Haiku 后，真实 CLI 发往模型接口的 model 必须随之改变。
  DESKTOP_CLAUDE_MODEL({ request, finish }) {
    assert.match(String(request.model), /haiku/i, `界面换模未生效：${request.model}`)
    return finish('DESKTOP_CLAUDE_MODEL_OK')
  },
}

export const desktopMarkers = Object.keys(desktopScenarios)
