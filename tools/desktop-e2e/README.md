# 桌面端 GUI 验收环境

`serve.mjs` 在本机常驻一套真实 Control、回环 Mock LLM 与双入口 Worker（Codex、Claude），供安装版 ChatGPT.app（内置 Codex）经专用 SSH Host 接入。Worker 使用临时 HOME、虚拟密钥与 sandbox-exec 外连限制，不读取个人模型登录态，不访问公网模型。

## 启动

```sh
export PATH="$PWD/.local/toolchains/node-v24.14.0-darwin-arm64/bin:$(go env GOROOT)/bin:$PATH"
export TYRS_HAND_TEST_CODEX_BIN="$PWD/.local/toolchains/codex-0.157.1/bin/codex"
node tools/desktop-e2e/serve.mjs   # 就绪后输出 [desktop-e2e] ready {...}
```

- 适配器须位于 `protocol/adapter-lock.json` 固定的提交且工作区干净。
- Worker 根目录固定为 `/tmp/000-tyrs-desktop-e2e`（`--root` 可改），项目路径 `/private/tmp/000-tyrs-desktop-e2e/project` 跨次运行不变。
- 测试主机写入 `~/.ssh/config.d/tyrs-desktop-e2e`（别名 `tyrs-e2e-claude`、`tyrs-e2e-codex`，均指向 127.0.0.1，主机密钥只写临时 known_hosts）。需在 `~/.ssh/config` 顶部一次性加入 `Include ~/.ssh/config.d/tyrs-desktop-e2e`；片段缺失时该行被忽略。
- Ctrl+C 退出：校验真实 wire schema，删除 ssh 片段，清理容器与临时目录。

## ChatGPT.app 首次配置

1. 设置 → 连接 → SSH → 添加：从发现列表勾选 `tyrs-e2e-claude`、`tyrs-e2e-codex`。
2. 在对应主机行点“添加项目文件夹”，路径填 `/private/tmp/000-tyrs-desktop-e2e/project`，信任文件夹。
3. 之后每次启动 `serve.mjs`，两台主机会自动重连，项目无需重建。

## 场景

在该项目的新聊天中发送标记词（与移动端共用 Mock LLM 场景）：

| 标记 | 入口 | 操作与断言 |
| --- | --- | --- |
| `MOBILE_CLAUDE_CHAT` | Claude | 回复 `_OK` |
| `MOBILE_CLAUDE_APPROVAL` | Claude，请求批准 | 点“允许一次”，文件落盘 |
| `MOBILE_CLAUDE_DENY` | Claude，请求批准 | 点“拒绝”，无文件 |
| `MOBILE_CLAUDE_FULL` | Claude，完全访问（新聊天） | 无审批直接落盘 |
| `MOBILE_CLAUDE_PLAN` | Claude，“+ → 计划模式” | 选 Blue，执行计划，文件内容为 Blue |
| `MOBILE_CODEX_CHAT` | Codex | 回复 `_OK` |

同一会话内工具调用 ID 固定，重复同一场景需新开聊天。

## 自动执行

`serve.mjs` 就绪后另开终端运行（需已安装 Peekaboo 并授予终端辅助功能、屏幕录制权限）：

```sh
node tools/desktop-e2e/gui.mjs --screenshots <证据目录>/gui   # 可加 --only MOBILE_CLAUDE_CHAT,...
```

脚本逐场景新开聊天、选择项目与权限、发送标记词、处理审批与计划问答，并等待界面出现回复。随后对 `serve.mjs` 按 Ctrl+C：
模型断言（6 个场景的真实终态、文件副作用、工具结果回模）与 wire schema 全部通过时输出 `[desktop-e2e] passed` 并以 0 退出。

操作要点：Electron 按钮需前台真实点击；中文输入法会改写逐字键入，文本一律粘贴；权限菜单不在辅助功能树中，按“更改权限”按钮的相对位置点击；
用户可能同时使用 ChatGPT.app，冲突时应退避。
