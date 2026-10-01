// 桌面 GUI 验收的 Peekaboo 驱动：定位安装版 ChatGPT.app 主窗口、截图取元素、点击、粘贴与等待。
// 优先按辅助功能标签定位，弹层用键盘操作；不进入辅助功能树的菜单才按锚点按钮的相对位置点击。
// 操作要点：Electron 按钮需前台真实点击；中文输入法会改写逐字键入，文本一律粘贴。
import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { resolve } from 'node:path'

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))

export function createDriver({ shots }) {
  const peekaboo = process.env.PEEKABOO_BIN ??
    resolve(homedir(), '.local/share/peekaboo/node_modules/@steipete/peekaboo/peekaboo')
  mkdirSync(shots, { recursive: true })
  const run = (args) => execFileSync(peekaboo, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'pipe'] })
  let window
  let step = 0

  function mainWindow() {
    const { data } = JSON.parse(run(['window', 'list', '--app', 'ChatGPT', '--json']))
    const windows = data.windows.filter((item) => item.observation_capability === 'combined_eligible')
    if (!windows.length) throw new Error('ChatGPT.app 没有可观察的主窗口')
    window = windows.sort((a, b) => b.bounds.width * b.bounds.height - a.bounds.width * a.bounds.height)[0]
    return window
  }
  const target = () => ['--app', 'ChatGPT', '--window-id', String(window.window_id)]

  // ScreenCaptureKit 可能被另一 Peekaboo 进程（如 MCP 服务）占用，此时回退 classic 引擎。
  function see(label) {
    const path = resolve(shots, `${String(++step).padStart(3, '0')}-${label}.png`)
    const args = ['see', ...target(), '--json', '--depth', '60', '--max-elements', '3000', '--max-children', '400', '--path', path]
    try {
      return JSON.parse(run(args)).data
    } catch {
      return JSON.parse(run([...args, '--capture-engine', 'classic'])).data
    }
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
  // Peekaboo 事后恢复原剪贴板；粘贴结果由界面确认。
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

  // 同一快照中须同时出现全部文本（用于证明回合进行中的中间状态）。
  async function waitForAll(label, texts, timeoutMs = 30_000) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const data = see(label)
      if (texts.every((expected) => find(data, (value) => value.includes(expected)))) return data
      if (Date.now() > deadline) throw new Error(`等待界面超时：${label}`)
      await sleep(700)
    }
  }

  // 断言一段时间内界面始终不出现某文本（如审批）。
  async function assertAbsent(label, predicate, durationMs) {
    const deadline = Date.now() + durationMs
    while (Date.now() < deadline) {
      const data = see(label)
      if (find(data, predicate)) throw new Error(`界面不应出现：${label}`)
      await sleep(1_000)
    }
  }

  async function newChat({ project, host }) {
    const home = await waitFor('home', (value) => value === '新聊天')
    click(home.data, home.element)
    const chat = await waitFor('new-chat', (value) => value.startsWith('切换项目'))
    click(chat.data, chat.element)
    await sleep(800)
    paste(project)
    await sleep(800)
    press('return')
    await waitFor('project', (value) => value === `切换项目：${project} · ${host}`, 15_000)
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

  return { mainWindow, window: () => window, see, find, click, clickNear, press, paste, waitFor, waitForAll,
    assertAbsent, newChat, setPermission, sleep }
}
