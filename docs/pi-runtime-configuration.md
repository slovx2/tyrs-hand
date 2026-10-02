# Pi 运行时制品与安装约定

Pi 入口默认关闭，独立 SSH 端口为 `:3334`，与 Codex、Claude 共用 Worker 身份和客户端授权。Pi 使用官方 SDK 与原生配置、会话格式；MCP 管理面板不桥接，仍由原生 `mcp.json` 和官方扩展管理。

## 固定版本与制品

`protocol/adapter-lock.json` 固定适配器提交、Node、SDK、CLI 和插件版本。`internal-deploy.yml` 的 `pi-runtime` job 与 Control、Worker、Claude 制品同一次运行，产出 `pi-runtime-linux-amd64` artifact：

```text
pi-codex_<adapter-commit>_linux_amd64.tar.gz
pi-codex_<adapter-commit>_linux_amd64.tar.gz.sha256
pi-codex_<adapter-commit>_linux_amd64.tar.gz.sigstore.json
```

打包脚本默认拒绝提交不符或未提交源码；`--local-acceptance` 仅用于本地验收，文件名含 `_local_`，不能替代正式制品。制品自检覆盖解包启动、实际版本、PTY、文件操作与真实 SDK/mock provider。正式流水线再校验 SHA-256，并通过 Sigstore 签名、验签。

Linux amd64 制品按依赖包的 `os`、`cpu`、`libc` 声明裁剪其他平台包（含嵌套的 esbuild 平台二进制），解包后再次断言没有非目标平台包，并运行每份 esbuild 的 TypeScript 转换自检。不使用 `--omit=optional`，以保留 codemode 所需的本机二进制；SDK 自带的嵌套 JavaScript 依赖和 WASM 保留，避免改变官方模块解析和原生功能。

## 宿主安装

Worker 安装器不负责安装 Pi CLI 或解包运行时。由部署人员按 Worker 用户原有 npm prefix 独立安装固定 CLI：

```sh
npm install --global --prefix "$HOME/.local" @earendil-works/pi-coding-agent@0.99.1
"$HOME/.local/bin/pi" --version
```

系统安装可使用 `/usr/local` prefix。宿主 CLI 必须为 `0.99.1`；适配器仍自带 Node `24.14.0`，不要求宿主 Node 恰好等于此版本。

验签后，把 tarball 解包到 `/usr/local/lib/tyrs-hand/pi-runtime/<adapter-commit>/`，创建 `/usr/local/libexec/tyrs-hand-pi` 可执行包装脚本：

```sh
#!/bin/sh
exec /usr/local/lib/tyrs-hand/pi-runtime/<adapter-commit>/pi-runtime/bin/pi-codex "$@"
```

使用包装脚本而不是软链，保证入口按自身路径找到制品文件。修改已有 Worker 配置前备份，并保留最近 4 份历史：

```sh
TYRS_HAND_WORKER_PI_ENABLED=true
TYRS_HAND_WORKER_PI_BIN=/usr/local/libexec/tyrs-hand-pi
TYRS_HAND_WORKER_PI_SSH_LISTEN_ADDR=:3334
PI_CLI=/absolute/path/to/pi
```

Pi 使用 Worker 用户原生 agentDir；可设置 `PI_CODING_AGENT_DIR`，不要覆盖已有凭据、模型配置、扩展或 JSONL。以 Worker 用户和相同环境执行：

```sh
PI_CLI=/absolute/path/to/pi /usr/local/libexec/tyrs-hand-pi --runtime-info
/usr/local/libexec/tyrs-hand-pi --pty-self-check
ssh -i <authorized-private-key> -p 3334 developer@<worker-host> tyrs-hand-worker runtime info
```

确认 `engine=pi`、`status=running`、workerId 与其他入口相同，版本符合锁文件，Control 心跳正常。

## 验收状态

2026-10-01，真实 ChatGPT.app 桌面全量 9/9 场景通过，wire schema 错误 0。移动端 UI 本期由用户明确暂不验收，属于未覆盖项，不是本期阻塞。MCP 面板属于范围边界。

`releaseReady` 暂保留 false：代码与桌面已验收，正式固定提交的 Linux 制品及签名仍需由内部构建任务核验；该值不阻止构建。本次代码交付不触发内部部署工作流，也不执行生产安装。
