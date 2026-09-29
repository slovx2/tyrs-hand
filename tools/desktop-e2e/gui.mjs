// 用 Peekaboo 驱动安装版 ChatGPT.app 执行桌面验收场景。前提：serve.mjs 已就绪，README 所述测试主机与项目已在
// ChatGPT.app 中配置。GUI 只负责真实用户操作并等待界面出现回复；通过与否由 serve.mjs 退出时的模型断言与 wire 校验判定。
// 优先按辅助功能标签定位，弹层用键盘操作；只有权限菜单按其锚点按钮的相对位置点击（该菜单不进入辅助功能树）。
import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { resolve } from 'node:path'

const peekaboo = process.env.PEEKABOO_BIN ??
  resolve(homedir(), '.local/share/peekaboo/node_modules/@steipete/peekaboo/peekaboo')
const argumentsMap = new Map()
for (let index = 2; index < process.argv.length; index += 2) argumentsMap.set(process.argv[index], process.argv[index + 1])
const shots = argumentsMap.get('--screenshots') ?? resolve('.local/e2e/desktop-gui-screenshots')
mkdirSync(shots, { recursive: true })

const claude = { project: 'desktop-e2e', host: 'tyrs-e2e-claude' }
const codex = { project: 'desktop-e2e-codex', host: 'tyrs-e2e-codex' }
const scenarios = [
  { marker: 'MOBILE_CLAUDE_CHAT', ...claude, permission: 'ask' },
  { marker: 'MOBILE_CLAUDE_APPROVAL', ...claude, permission: 'ask', approval: 'accept' },
  { marker: 'MOBILE_CLAUDE_DENY', ...claude, permission: 'ask', approval: 'decline' },
  { marker: 'MOBILE_CLAUDE_FULL', ...claude, permission: 'full' },
  { marker: 'MOBILE_CLAUDE_PLAN', ...claude, permission: 'full', plan: true },
  { marker: 'MOBILE_CODEX_CHAT', ...codex },
]
const only = argumentsMap.get('--only')?.split(',')

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))
const run = (args) => execFileSync(peekaboo, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024,
  stdio: ['ignore', 'pipe', 'pipe'] })

function mainWindow() {
  const { data } = JSON.parse(run(['window', 'list', '--app', 'ChatGPT', '--json']))
  const windows = data.windows.filter((item) => item.observation_capability === 'combined_eligible')
  if (!windows.length) throw new Error('ChatGPT.app 没有可观察的主窗口')
  return windows.sort((a, b) => b.bounds.width * b.bounds.height - a.bounds.width * a.bounds.height)[0]
}

let window = mainWindow()
const target = () => ['--app', 'ChatGPT', '--window-id', String(window.window_id)]
let step = 0

function see(label) {
  const path = resolve(shots, `${String(++step).padStart(3, '0')}-${label}.png`)
  const { data } = JSON.parse(run(['see', ...target(), '--json', '--depth', '60', '--max-elements', '3000',
    '--max-children', '400', '--path', path]))
  return data
}

// 标签、标题与值分别匹配；同一元素常在多个字段重复相同文本。
function find(data, predicate) {
  return data.ui_elements.find((element) =>
    [element.label, element.title, element.value].some((value) => typeof value === 'string' && predicate(value, element)))
}

function click(data, element) {
  run(['click', '--snapshot', data.snapshot_id, '--on', element.id, ...target(), '--foreground',
    '--input-strategy', 'synthOnly'])
}

// 元素边界为屏幕坐标，--at 前台点击为窗口坐标。
function clickNear(element, dx, dy) {
  const x = Math.round(element.bounds.x - window.bounds.x + element.bounds.width / 2 + dx)
  const y = Math.round(element.bounds.y - window.bounds.y + element.bounds.height / 2 + dy)
  run(['click', '--at', `${x},${y}`, ...target(), '--foreground', '--input-strategy', 'synthOnly'])
}

const press = (keys) => run(['press', keys, ...target(), '--foreground'])
// 中文输入法会改写逐字键入，文本一律经剪贴板粘贴（Peekaboo 事后恢复原剪贴板）。
const paste = (value) => { try { run(['paste', value, ...target(), '--foreground']) } catch { /* 结果由界面确认 */ } }

async function waitFor(label, predicate, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const data = see(label)
    const element = find(data, predicate)
    if (element) return { data, element }
    if (Date.now() > deadline) throw new Error(`等待界面超时：${label}`)
    await sleep(1_500)
  }
}

async function newChat(scenario) {
  const home = await waitFor('home', (value) => value === '新聊天')
  click(home.data, home.element)
  const chat = await waitFor('new-chat', (value) => value.startsWith('切换项目'))
  click(chat.data, chat.element)
  await sleep(800)
  paste(scenario.project)
  await sleep(800)
  press('return')
  await waitFor('project', (value) => value === `切换项目：${scenario.project} · ${scenario.host}`, 15_000)
}

async function setPermission(permission) {
  if (!permission) return
  const { element } = await waitFor('permission', (value) => value === '更改权限')
  // 菜单自下而上固定为：完全访问权限、帮我批准、请求批准。
  clickNear(element, 0, 0)
  await sleep(800)
  clickNear(element, 20, permission === 'full' ? -48 : -132)
  await sleep(1_000)
  if (permission !== 'full') return
  const data = see('full-access-confirm')
  const confirm = find(data, (value, candidate) => value === '确认' && candidate.role === 'button')
  if (confirm) click(data, confirm)
  else press('return')
  await sleep(800)
}

async function send(scenario) {
  const { data, element } = await waitFor('composer', (value) => value === '随心输入')
  click(data, element)
  if (scenario.plan) {
    paste('/plan')
    await sleep(1_000)
    press('return')
    await sleep(600)
  }
  paste(scenario.marker)
  await sleep(500)
  press('return')
}

async function interact(scenario) {
  if (scenario.approval) {
    await waitFor('approval', (value) => value.includes('允许一次'))
    press(scenario.approval === 'accept' ? 'return' : 'escape')
  }
  if (scenario.plan) {
    await waitFor('plan-question', (value) => value.includes('Choose a color'))
    press('1')
    await waitFor('plan-exit', (value) => value.includes('执行计划'))
    press('1')
  }
  await waitFor('reply', (value) => value === `${scenario.marker}_OK`, 120_000)
}

for (const scenario of scenarios.filter((item) => !only || only.includes(item.marker))) {
  window = mainWindow()
  console.log(`[desktop-gui] ${scenario.marker}`)
  await newChat(scenario)
  await setPermission(scenario.permission)
  await send(scenario)
  await interact(scenario)
  console.log(`[desktop-gui] ${scenario.marker} 界面已出现回复`)
}
console.log('[desktop-gui] 全部场景已在 GUI 执行；以 serve.mjs 退出结果为准')
