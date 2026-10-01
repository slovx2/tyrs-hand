// Pi 桌面验收场景：模型侧按标记返回固定回复（openai-completions 形态），工具名使用 Pi 原生的 bash/write、
// Plan 插件的 plan_mode_question/plan_mode_complete 与 gotgenes 的 subagent。界面断言见 gui.mjs，协议断言见 serve.mjs。
// Pi 原生没有计划模式以外的提问工具，也不因客户端权限档位增加审批；场景按原生能力设计，不向 Codex 补齐。
import assert from 'node:assert/strict'

export const steerPayload = 'PI_STEER_PAYLOAD_3K'
export const progressNote = 'DESKTOP_PI_PROGRESS_NOTE'
export const toolOutput = 'DESKTOP_PI_TOOL_STDOUT'
export const thinkingBody = 'DESKTOP_PI_THINKING_BODY'
export const planOutput = 'DESKTOP_PI_PLAN_OUTPUT'
export const childResult = 'DESKTOP_PI_CHILD_RESULT'
// models.json 中的两个模型；默认模型为第一项，换模场景切到第二项。
export const piModels = [
  { id: 'pi-main', name: 'Pi Main' },
  { id: 'pi-alt', name: 'Pi Alt' },
]
const sleep = (ms) => new Promise((done) => setTimeout(done, ms))

export const piScenarios = {
  DESKTOP_PI_CHAT: ({ finish }) => finish('DESKTOP_PI_CHAT_OK'),
  // 中间过程：过程说明与持续数秒的真实 bash 命令须在运行期间同时显示，结束后可展开查看真实输出。
  DESKTOP_PI_TOOLS({ request, result, finish, tool }) {
    const found = result('call_pi_tools')
    if (!found) {
      assert.ok(request.tools?.some((entry) => entry.function?.name === 'bash'), 'Pi 未声明原生 bash 工具')
      return [{ type: 'text', text: progressNote }, tool('bash', 'call_pi_tools', { command: `sleep 6; echo ${toolOutput}` })]
    }
    assert.match(found.text, new RegExp(toolOutput), '真实命令输出必须回到模型')
    return finish('DESKTOP_PI_TOOLS_OK')
  },
  // 停止：模型请求挂起时由用户停止；迟到的回复不能进入界面或历史（wire 复核）。
  async DESKTOP_PI_STOP({ complete, closed }) {
    complete()
    await Promise.race([sleep(45_000), closed])
    return [{ type: 'text', text: 'DESKTOP_PI_STOP_TOO_LATE' }]
  },
  // 思考显示：推理内容之后 6 秒无其他输出，思考即为回合中最新的条目。
  DESKTOP_PI_THINK({ result, finish, tool }) {
    if (!result('call_pi_think')) return [
      { type: 'reasoning', text: `**Checking Pi thinking**\n\n${thinkingBody}`, pauseAfterMs: 6_000 },
      tool('bash', 'call_pi_think', { command: 'echo PI_THINK_WINDOW' }),
    ]
    return finish('DESKTOP_PI_THINK_OK')
  },
  // 权限档位：在“请求批准”档位下 Pi 原生 write 直接落盘，界面不出现审批。
  DESKTOP_PI_WRITE({ result, finish, tool, workspace }) {
    const found = result('call_pi_write')
    if (!found) return [tool('write', 'call_pi_write', { path: `${workspace}/DESKTOP_PI_WRITE.txt`, content: 'DESKTOP_PI_WRITE' })]
    assert.doesNotMatch(found.text, /error/i, `write 未成功：${found.text}`)
    return finish('DESKTOP_PI_WRITE_OK')
  },
  // Plan：计划模式内用插件问答选 Blue，提交计划；界面执行计划后在实施阶段写入 Blue。
  // 实施可能由插件以新上下文转交计划，此时只有计划正文中的标记，按 aliases 归入本场景；阶段由服务端状态推进。
  DESKTOP_PI_PLAN({ result, finish, tool, workspace, state }) {
    if (!state.stage) {
      const answer = result('call_pi_plan_question')
      if (!answer) return [tool('plan_mode_question', 'call_pi_plan_question', { questions: [{ id: 'color', header: 'Color',
        question: 'Choose a color', options: [{ label: 'Blue', description: 'Write Blue' }, { label: 'Red', description: 'Write Red' }] }] })]
      assert.match(answer.text, /Blue/, '模型必须收到用户在界面选择的 Blue')
      state.stage = 'submitted'
      return [tool('plan_mode_complete', 'call_pi_plan_complete', { plan: `# Plan\n\n${planOutput}: write Blue after confirmation.` })]
    }
    const written = result('call_pi_plan_write')
    if (!written) {
      state.stage = 'implementing'
      return [tool('write', 'call_pi_plan_write', { path: `${workspace}/DESKTOP_PI_PLAN.txt`, content: 'Blue' })]
    }
    assert.doesNotMatch(written.text, /error/i, `实施阶段写入失败：${written.text}`)
    return finish('DESKTOP_PI_PLAN_OK')
  },
  // steer：命令运行期间追加消息，工具结果之后的下一次模型请求必须包含该消息。
  DESKTOP_PI_STEER({ user, result, finish, tool }) {
    if (!result('call_pi_steer')) return [tool('bash', 'call_pi_steer', { command: 'sleep 20; echo PI_STEER_WINDOW' })]
    assert.ok(JSON.stringify(user).includes(steerPayload), 'steer 消息必须进入同一回合的下一次模型请求')
    return finish('DESKTOP_PI_STEER_OK')
  },
  // 换模：界面选择 Pi Alt 后，Pi 发往模型接口的 model 必须随之改变。
  DESKTOP_PI_MODEL({ request, finish }) {
    assert.equal(request.model, piModels[1].id, `界面换模未生效：${request.model}`)
    return finish('DESKTOP_PI_MODEL_OK')
  },
  // 子代理：gotgenes 内置 general-purpose 真实调用模型，结果回到主代理；界面展示委派过程。
  DESKTOP_PI_SUBAGENT({ result, finish, tool }) {
    const found = result('call_pi_subagent')
    if (!found) return [tool('subagent', 'call_pi_subagent', { subagent_type: 'general-purpose',
      description: 'Pi desktop child', prompt: 'DESKTOP_PI_SUBAGENT_CHILD: reply with the child result.', run_in_background: false })]
    assert.match(found.text, new RegExp(childResult), '子代理结果必须回到主代理')
    return finish('DESKTOP_PI_SUBAGENT_OK')
  },
  DESKTOP_PI_SUBAGENT_CHILD: () => [{ type: 'text', text: childResult }],
}

// 计划正文与子代理提示中的标记归入所属场景。
export const piAliases = { DESKTOP_PI_PLAN_OUTPUT: 'DESKTOP_PI_PLAN' }

// 文件副作用，由 serve.mjs 退出时核对。
export const piEffects = {
  DESKTOP_PI_WRITE: { file: 'DESKTOP_PI_WRITE.txt', content: 'DESKTOP_PI_WRITE' },
  DESKTOP_PI_PLAN: { file: 'DESKTOP_PI_PLAN.txt', content: 'Blue' },
}

// 冒烟覆盖连接、回合、工具过程与停止，日常改动只跑冒烟；全量用于里程碑验收。
export const piSuites = {
  smoke: ['DESKTOP_PI_CHAT', 'DESKTOP_PI_TOOLS', 'DESKTOP_PI_STOP'],
  full: ['DESKTOP_PI_CHAT', 'DESKTOP_PI_TOOLS', 'DESKTOP_PI_STOP', 'DESKTOP_PI_THINK', 'DESKTOP_PI_WRITE',
    'DESKTOP_PI_PLAN', 'DESKTOP_PI_STEER', 'DESKTOP_PI_MODEL', 'DESKTOP_PI_SUBAGENT'],
}

export function selectMarkers({ suite = 'smoke', only } = {}) {
  const markers = only ? only.split(',') : piSuites[suite]
  assert.ok(markers, `未知套件：${suite}`)
  for (const marker of markers) assert.ok(piSuites.full.includes(marker), `未登记的 Pi 场景：${marker}`)
  return markers
}
