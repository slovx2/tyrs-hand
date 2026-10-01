// 用 Peekaboo 驱动安装版 ChatGPT.app 执行 Pi 桌面验收场景。前提：pi/serve.mjs 已就绪，README 所述 `tyrs-e2e-pi`
// 主机与项目已在 ChatGPT.app 中配置。GUI 只负责真实用户操作与界面断言；通过与否以 serve.mjs 退出时的模型断言与 wire 校验为准。
//   node tools/desktop-e2e/pi/gui.mjs [--suite smoke|full] [--only DESKTOP_PI_CHAT,...] [--screenshots <目录>]
import { resolve } from 'node:path'
import { createDriver } from '../lib/driver.mjs'
import { piModels, planOutput, progressNote, selectMarkers, steerPayload, thinkingBody, toolOutput } from './scenarios.mjs'

const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) argumentsMap.set(process.argv[index], process.argv[index + 1])
const ui = createDriver({ shots: argumentsMap.get('--screenshots') ?? resolve('.local/e2e/desktop-pi-gui-screenshots') })
const pi = { project: argumentsMap.get('--project') ?? 'desktop-e2e-pi', host: argumentsMap.get('--host') ?? 'tyrs-e2e-pi' }

// Pi 不因权限档位增加审批；除专门验证该行为的场景外统一使用完全访问，与日常使用一致。
const scenarios = {
  DESKTOP_PI_CHAT: { permission: 'full' },
  DESKTOP_PI_TOOLS: { permission: 'full', tools: true },
  DESKTOP_PI_STOP: { permission: 'full', stop: true },
  DESKTOP_PI_THINK: { permission: 'full', thinking: true },
  DESKTOP_PI_WRITE: { permission: 'ask', noApproval: true },
  DESKTOP_PI_PLAN: { permission: 'full', plan: true },
  DESKTOP_PI_STEER: { permission: 'full', steer: true },
  DESKTOP_PI_MODEL: { permission: 'full', model: piModels[1].name },
  DESKTOP_PI_SUBAGENT: { permission: 'full', subagent: true },
}

// 模型菜单不进入辅助功能树：先点模型按钮展开，再以键盘逐项移动并回车，最后以按钮标签确认结果。
async function selectModel(name) {
  const isModel = (value, element) => element.role === 'button' && piModels.some((model) => value.includes(model.name))
  const chip = await ui.waitFor('model-chip', isModel)
  if (chip.element.label?.includes(name) || chip.element.title?.includes(name)) return
  ui.clickNear(chip.element, 0, 0)
  await ui.sleep(900)
  // 弹层首屏为强度滑块，其上方的“当前模型 ›”入口不进入辅助功能树：按弹层分组顶部的相对位置点开模型列表。
  const popup = ui.see('model-popup')
  const group = ui.find(popup, (value, element) => element.role === 'group' &&
    (value === '选择强度' || piModels.some((model) => value.startsWith(model.name))))
  if (!group) throw new Error('未找到模型弹层，需按截图校准')
  ui.clickNear(group, 0, 38 - group.bounds.height / 2)
  await ui.sleep(900)
  const data = ui.see('model-menu')
  const item = ui.find(data, (value, element) => element.role !== 'button' && value === name)
  if (item) ui.click(data, item)
  else throw new Error(`模型菜单中未找到 ${name}，需按截图校准菜单定位`)
  await ui.sleep(700)
  ui.press('escape')
  await ui.waitFor('model-selected', (value, element) => element.role === 'button' && value.includes(name), 10_000)
}

async function send(marker, scenario) {
  if (scenario.model) await selectModel(scenario.model)
  const { data, element } = await ui.waitFor('composer', (value) => value === '随心输入')
  scenario.composer = element
  ui.click(data, element)
  if (scenario.plan) {
    ui.paste('/plan')
    await ui.sleep(1_000)
    ui.press('return')
    await ui.sleep(600)
  }
  ui.paste(marker)
  await ui.sleep(500)
  ui.press('return')
}

async function interact(marker, scenario) {
  if (scenario.tools) await ui.waitForAll('tools-running', [progressNote, `正在运行 sleep 6; echo ${toolOutput}`])
  if (scenario.thinking) await ui.waitFor('thinking', (value) => value.includes(thinkingBody), 15_000)
  if (scenario.noApproval) await ui.assertAbsent('no-approval', (value) => value.includes('允许一次'), 5_000)
  if (scenario.plan) {
    // 计划模式内插件问答经客户端回答；计划正文显示后由用户确认执行。
    // 插件以终端菜单格式提交选项（“1. Blue — Write Blue”），须点击选项作答（数字键不会提交答案）。
    const question = await ui.waitFor('plan-question', (value, candidate) =>
      candidate.role === 'checkbox' && value.startsWith('1. Blue — Write Blue'))
    ui.click(question.data, question.element)
    // 计划卡片显示后，插件原生菜单（Pi 行为，非 Codex 的“执行计划”）询问下一步，选择就地实施。
    await ui.waitForAll('plan-output', [planOutput, 'Proposed plan ready'], 60_000)
    const implement = await ui.waitFor('plan-menu', (value, candidate) =>
      candidate.role === 'checkbox' && value.startsWith('Implement here'))
    ui.click(implement.data, implement.element)
  }
  if (scenario.stop) {
    const { data, element } = await ui.waitFor('running', (value, candidate) => candidate.role === 'button' && value === '停止')
    await ui.sleep(3_000)
    ui.click(data, element)
    await ui.waitFor('stopped', (value) => /后停止了$/.test(value), 30_000)
    return
  }
  if (scenario.steer) {
    // 命令运行期间立即插入；输入框位置沿用发送前定位的结果。
    await ui.waitFor('steer-window', (value) => value.includes('正在运行 sleep 20; echo PI_STEER_WINDOW'), 30_000)
    ui.clickNear(scenario.composer, 0, 0)
    ui.paste(steerPayload)
    ui.press('return')
  }
  await ui.waitFor('reply', (value) => value === `${marker}_OK`, 120_000)
  if (scenario.tools) {
    // 回合结束后折叠的过程仍可展开查看工具调用及其真实输出。
    const summary = await ui.waitFor('process-summary', (value, element) => element.role === 'button' && value.startsWith('用时'))
    ui.click(summary.data, summary.element)
    const command = await ui.waitFor('tool-item', (value, element) => element.role === 'button' &&
      value === `已运行 sleep 6; echo ${toolOutput}`)
    ui.click(command.data, command.element)
    await ui.waitFor('tool-output', (value, element) => element.role !== 'button' && value === toolOutput, 15_000)
  }
  if (scenario.subagent) {
    // 委派过程须在界面可见（展开过程后显示“已创建 1 个智能体”）；结果已经回到主代理（模型断言）。
    const summary = await ui.waitFor('subagent-summary', (value, element) => element.role === 'button' && value.startsWith('用时'))
    ui.click(summary.data, summary.element)
    await ui.waitFor('subagent', (value, element) => element.role === 'button' && /已创建\s*1\s*个智能体/.test(value), 15_000)
  }
}

const markers = selectMarkers({ suite: argumentsMap.get('--suite') ?? 'smoke', only: argumentsMap.get('--only') })
for (const marker of markers) {
  const scenario = scenarios[marker]
  ui.mainWindow()
  console.log(`[desktop-pi-gui] ${marker}`)
  await ui.newChat(pi)
  await ui.setPermission(scenario.permission)
  await send(marker, scenario)
  await interact(marker, scenario)
  console.log(`[desktop-pi-gui] ${marker} 界面已出现回复`)
}
// 换模会持久化所选模型；恢复默认，避免影响后续新建聊天。
if (markers.includes('DESKTOP_PI_MODEL')) {
  ui.mainWindow()
  await ui.newChat(pi)
  await selectModel(piModels[0].name)
}
console.log(`[desktop-pi-gui] 已执行 ${markers.join(',')}；以 serve.mjs 退出结果为准（启动时用相同 --suite/--only）`)
