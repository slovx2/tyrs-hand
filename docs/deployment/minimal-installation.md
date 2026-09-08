# 最小安装指引

Tyrs Hand 由公网 Control 与一台或多台宿主 Worker 组成。Control 以签名镜像发布；Worker 以签名薄二进制安装为 systemd service 或 LaunchDaemon。

## 1. Control

Control 主机需要：

- Docker Engine 与 Docker Compose
- PostgreSQL、Redis（示例 Compose 可直接启动）
- 指向 Control 的 HTTPS 域名

```bash
cp .env.example .env
install -d -m 0700 .local/secrets
openssl rand -base64 32 > .local/secrets/master_key
openssl rand -hex 32 > .local/secrets/postgres_password
chmod 0600 .env .local/secrets/*

docker compose -f compose.yaml -f compose.production.yaml up -d postgres redis
docker compose -f compose.yaml -f compose.production.yaml --profile tools run --rm admin migrate
docker compose -f compose.yaml -f compose.production.yaml up -d server discord
```

反向代理必须转发管理 API 和全部 `/worker/v1/*` 路径。Worker API 是直接 REST 接口，包含注册、心跳、领取、Workspace、Desktop、Thread、Turn、Run、Blob、Tool、Git 与 SSH 操作；完整路径见 `api/openapi.yaml`。

Control 管理 Discord、Worker、Workspace、任务参数和出站 SSH。Codex Provider、API Key、ChatGPT 登录态、Base URL、代理与模型目录不属于 Control 配置。

### 配置备份

修改生产 `.env` 前必须创建带时间戳的备份，并只保留最近四份。数据库、Control 配置和 Worker 配置应在每次发布前独立备份。镜像使用不可变 Digest，不使用 `latest`。

## 2. Worker 宿主依赖

支持：

- Linux amd64 / arm64
- macOS amd64 / arm64

宿主必须安装满足 `deploy/worker/dependencies.json` 的工具：

- Codex CLI `>= 0.147.0`
- Git `>= 2.39.0`
- OpenSSH Client（`ssh`、`scp`、`ssh-agent`）`>= 9.2.0`
- `curl`、`tar`、`sudo` 与可执行的用户 Shell
- 从 GitHub Release 下载时使用 Cosign `3.9.2`

SSH Server、SFTP Server 和 PTY 支持已编译进 Worker。宿主自行安装 Codex、Git、SSH、Browser MCP 和业务所需语言工具链。

## 3. 机器用户与目录

每个 Worker 绑定一个真实 OS 用户：

- `HOME` 是该用户 Home。
- `CODEX_HOME` 取服务环境中的 `CODEX_HOME`，未设置时为 `~/.codex`。
- 项目根目录固定为 `~/tyrs-hand/workspaces`，每个一级目录是一个项目。
- Linux 状态目录为 `~/.local/share/tyrs-hand/worker`。
- macOS 状态目录为 `~/Library/Application Support/Tyrs Hand/worker`。

Worker 直接使用该 Home 中已有的会话、登录态、配置和 Skill，不复制或改写 Codex Home。

## 4. 安装 Worker

在管理后台创建 Worker，选择角色与并发上限，生成一次性 Enrollment Token。若 Worker 承载 Discord、Mobile 或 Desktop 会话，再在 Worker 页面为它创建唯一 Workspace。

准备 Codex Desktop 客户端公钥文件，每行一把标准 OpenSSH 公钥；允许添加行尾名称，不允许 `command=` 等 key option。

```bash
sudo env \
  TYRS_HAND_RELEASE_VERSION=v0.2.0 \
  TYRS_HAND_WORKER_CONTROL_URL=https://agent.example.com \
  TYRS_HAND_WORKER_ENROLLMENT_TOKEN=<one-time-token> \
  TYRS_HAND_WORKER_PUBLIC_KEYS_FILE=/path/to/authorized_keys \
  TYRS_HAND_WORKER_USER=<os-user> \
  sh deploy/worker/install.sh
```

安装脚本会：

1. 下载当前 OS/架构的 tarball、`.sha256` 和 `.sigstore.json`。
2. 校验 SHA-256、GitHub Actions OIDC issuer 与 Release Workflow 身份。
3. 备份现有 Worker 二进制，最多保留四份。
4. 安装 `/usr/local/bin/tyrs-hand-worker` 与运行包装脚本。
5. 创建状态目录、Workspace 根目录和多客户端授权公钥文件。
6. 备份 `/etc/tyrs-hand/worker.env`，最多保留四份，再写入新配置。
7. 执行 `tyrs-hand-worker doctor`。
8. 安装并启动 Linux systemd unit 或 macOS LaunchDaemon。

注册成功后，一次性 Enrollment Token 会从配置中删除；长期 Worker Credential 以 `0600` 保存在用户状态目录。

```bash
sudo -u <os-user> /usr/local/libexec/tyrs-hand-worker-run doctor
```

`doctor` 会检查 Codex 最低版本、Home、Codex Home、Workspace、状态目录、SSH 公钥和必需命令。

## 5. Worker 配置

示例见 `deploy/worker/worker.env.example`。主要字段：

```dotenv
TYRS_HAND_WORKER_CONTROL_URL=https://agent.example.com
TYRS_HAND_WORKER_ROLE=all
TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS=6
TYRS_HAND_CODEX_BIN=/usr/local/bin/codex
TYRS_HAND_WORKER_CODEX_HOME=/home/worker/.codex
TYRS_HAND_WORKER_WORKSPACE_ROOT=/home/worker/tyrs-hand/workspaces
TYRS_HAND_WORKER_SSH_LISTEN_ADDR=:2222
TYRS_HAND_BROWSER_MCP_URL=http://127.0.0.1:8931/mcp
TYRS_HAND_BROWSER_MCP_TOKEN_FILE=/home/worker/.local/share/tyrs-hand/browser/token
TYRS_HAND_BROWSER_AGENT_ADDRESS=127.0.0.1:8934
TYRS_HAND_BROWSER_FILES_ROOT=/home/worker/.local/share/tyrs-hand/browser/files
TYRS_HAND_BROWSER_SERVICES_ROOT=/opt/tyrs-hand/browser-services
```

Provider、API Key、ChatGPT Auth、Base URL 与 Proxy 的真相源仍是机器用户自己的 Codex Home。Control 只通过认证 Worker WebSocket 读取和更新 Provider 非敏感字段及 `AGENTS.md`，不会保存配置正文或任何 `auth.json` token；Control 发起的 Codex OAuth device code 登录也由 Worker 写入自身 `auth.json`。

Provider 的 `base_url` 与 API Key 由 Control 通过认证 Worker 通道写入机器级 `/etc/environment`，其中 API Key 只使用环境变量 `TYRS_HAND_MODEL_API_KEY`，不会写入 `config.toml`。Linux systemd Worker、`tyrs-hand-worker-run` 和登录 Shell 都会加载该文件。为满足机器级环境变量语义，文件由 `root:<worker-group>` 持有并设置为 `0664`，因此本机所有用户均可读取；它不进入 Control、Worker 协议或任务快照。

Worker 启动时读取一次机器 Codex Home 和 `codex.env`，并为整个 Worker 生命周期启动唯一 Codex App Server。Control 配置变更不会自动重启，需使用控制台的“重启 Codex”按钮；ChatGPT OAuth 登录态不会改变显式配置的 Model Provider。

GitHub Webhook、GitHub Work Item 和 GitHub Agent 功能已停用；Desktop、Discord 与 Mobile 的未指定参数由机器 Codex Home 决定。

## 6. SSH 与多客户端

Worker 默认监听 `:2222`，支持：

- Shell、远程命令和有限环境变量
- PTY 与窗口尺寸更新
- SCP 与内置 SFTP
- 多公钥、多客户端并发
- Codex App Server 特殊通道
- Browser Agent 特殊通道

Codex Desktop 必须使用指向 Worker 监听端口的专用 SSH Host，不能选择宿主系统 SSH 的运维 Host。例如：

```sshconfig
Host tyrs-worker
  HostName worker.example.com
  Port 2222
  User worker
  IdentityFile ~/.ssh/tyrs-worker
```

Worker 会识别 Codex Desktop 的握手包装命令并直接接入唯一 AppServer Hub；无法识别的 App Server Proxy 命令会被拒绝，不会回退到 Shell 启动第二个 App Server。多个 Desktop 分别拥有独立 Hub Session，断开任一客户端不会关闭上游或影响 Worker 任务。

SSH 只接受 `session` channel，不支持本地、远程或动态端口转发。Agent 出站 SSH 是独立能力，由 Control 将 Credential 和 Host 下发给指定 Worker。

## 7. 宿主能力与 Workspace 绑定

已注册 Worker 无需绑定 Control Workspace，即可运行 Codex Desktop 对话、浏览器 MCP、文件交换、本地服务代理、本地 Git、图片生成、项目发现及模型目录。保持原有 Worker、SSH 公钥和浏览器 Token 认证；项目扫描仍限定于配置的工作目录及 Codex 注册项目。

Workspace 绑定负责身份、Forum 关联和 Control 同步。绑定同步始终运行，周期为心跳周期且最短 15 秒；绑定、解绑及负责人变化无需重启 Codex 或 Chrome。每个 turn 保留启动时的身份快照，新状态从下一 turn 生效。失效绑定不能发起新的 Control 自动任务或 Forum 发布。

Control 暂时不可达时保留最近确认的绑定；明确返回未绑定时清除缓存。已注册 Worker 即使没有缓存也可启动宿主能力。仅同步绑定生效后开始的活动，不批量导入已有对话历史。恢复已有对话时走官方 resume 加载 MCP，不改写历史、不复制对话；协议不能更新的历史动态工具列表不会被补写。

项目扫描响应同时包含 `workspace`（未绑定时为 `null`）和 `scan`。未绑定扫描不保存项目数据库记录；绑定后重新扫描建立正式关联。Worker 心跳以单份 `modelCatalog` 上报宿主模型目录，Control 按实际绑定生成客户端 Workspace 模型映射，未绑定在线 Worker 也参与全局目录。Control 与 Worker 应一起升级，不读取旧 metadata 格式，无新增数据库迁移。

### Browser

浏览器服务与扩展分开安装：Bridge/Browser Agent 安装后自动运行；Chrome 扩展在每台机器、每个默认 Profile 中首次手动加载一次。继续使用用户原有 Profile、登录态和标签页，不创建专用 Profile，不由安装器重启 Chrome。

#### Desktop Browser 首次安装顺序

首次安装一台 Worker 时，按以下顺序完成 Desktop Browser 配置：

1. 先安装并启动 Worker，确认 Worker 使用正确的 Control URL、Worker ID 和长期 Credential。
2. 等待 Worker 完成 Control 注册，并同步 SSH 配置。Desktop 使用的是 Worker 专用 SSH 入口（默认 `:2222`），不能使用宿主机运维 SSH 入口。
3. 在图形桌面会话中安装 Bridge/Browser Agent。Linux 使用 `install-host-release.sh`，macOS 使用 `install-macos-agent.sh`。安装器会将 Browser Agent 和 Bridge 放到固定目录并启动服务。
4. 在同一台机器的 Chrome 默认 Profile 中首次手动加载解压扩展。扩展目录和操作步骤见下文“Chrome 首次手动安装”。
5. 在 Codex Desktop 中使用指向该 Worker `:2222` 端口的专用 SSH Host，创建或打开该 Worker 的 Desktop Thread，再选择 Desktop Browser。

每台机器、每个 Chrome Profile 只需首次手动加载一次扩展。Worker、Bridge、Browser Agent 或 Chrome 重启后不需要重复安装；切换或删除 Chrome Profile 时，需要在新 Profile 中重新加载扩展。

#### 下载并放置扩展

Linux Worker 在已登录的图形桌面用户下运行 Bridge，需要 `/usr/local/bin/node`、`unzip` 以及该用户的 systemd user/D-Bus 会话。可通过 RDP 登录桌面后执行：

```bash
sudo bash deploy/browser/install-host-release.sh <桌面用户名> deploy/browser/browser-artifacts.lock.json
```

脚本按锁文件中的精确制品版本下载并校验 SHA-256，将 CRX 解包到 `/opt/tyrs-hand/browser/unpacked-extension`，校验扩展 ID 并保留 manifest 公钥。此目录不能删除或移动，也不能放在临时下载目录。脚本设置桌面用户归属，启动 Bridge；Chrome 尚未打开或未加载扩展时，服务启动不代表浏览器已经可用。没有用户 D-Bus 会话时应先登录图形桌面，脚本不能启动用户服务。

macOS 使用 `deploy/browser/install-macos-agent.sh install <agent.tgz> <ssh-host> <ssh-port> <ssh-user> <identity-file> <known-hosts-file> <extension-id>`。安装包应来自 `browser-artifacts.lock.json` 对应架构的 Browser Agent 制品并核对 SHA-256；其扩展解包到 `$HOME/Library/Application Support/Tyrs Hand/browser-agent/unpacked-extension`。脚本同时启动 Browser Agent，沿用现有 SSH 配置要求。

#### Chrome 首次手动安装

1. 在浏览器实际所在机器打开 Chrome，选择用户日常使用的默认 Profile；Linux Worker 可通过 RDP 操作。
2. 打开 `chrome://extensions`，开启“开发者模式”。
3. 点击“加载已解压的扩展程序”，选择上述本机固定目录（包含 `manifest.json` 的目录，不是 CRX 文件或压缩包）。
4. 核对扩展 ID 与锁文件 `extensionId` 一致，版本与解包目录 `manifest.json` 一致。
5. 在 Codex 中选择对应的 Worker 或 Desktop 浏览器，通过真实浏览器工具打开网页并读取标题、URL；随后打开 `http://127.0.0.1:8931/health`，确认 `status=ready`、`connected=true` 和扩展版本正确。

浏览器代理的 `8932` 端口按需启动。首次浏览器工具调用前，或 Bridge 重启后尚未调用工具时，`connected=false` 和扩展暂时报告连接被拒绝不能单独证明安装失败；应以真实工具调用结果及调用后的 health 验收。

无需手工填写 Token。Linux 的 `3rdparty.extensions` 托管配置只提供 Bridge 地址和扩展认证信息；macOS 沿用现有本地配置获取流程。安装器不再生成 `ExtensionInstallForcelist` 或 `ExtensionSettings` 强制安装规则，也不依赖本地 `update.xml` 自动安装/更新扩展。不要把策略文件或 Token 内容贴入日志或对话。

#### 升级与已有安装迁移

Worker、Bridge、Browser Agent、Chrome 重启不需要重复安装扩展。更新扩展文件后，在 `chrome://extensions` 点击该扩展的“重新加载”即可；未打包扩展不会通过 Chrome Web Store 自动更新。Profile 被删除或改用另一个 Profile 时，需要重新手动加载。扩展更新期间等待当前浏览器任务结束，再重新加载，避免打断操作。

已有 Linux 策略安装迁移时，先备份 `/etc/opt/chrome/policies/managed/tyrs-browser.json` 与 `/opt/tyrs-hand/browser/browser.env`，各保留最近 4 份。新安装器覆盖 Tyrs 专属策略文件，只保留 `3rdparty` 配置；在 `chrome://policy` 点击重新加载政策。若其他策略文件仍包含 Tyrs 扩展的旧安装规则，仅移除本扩展条目，保留其他扩展策略。原策略安装条目可能自动卸载；若仍残留旧条目，确认强制安装规则已撤销，再在扩展页移除旧条目，按上面的固定目录手动加载一次。不要删除 Chrome Profile 或用户标签页。迁移后核对 ID、版本及真实工具调用，而非仅检查 Bridge 进程。

Worker 任务直接访问宿主 Browser MCP 和宿主文件目录。Token 仅从 `TYRS_HAND_BROWSER_MCP_TOKEN_FILE` 读取，文件应为 Worker 用户所有且权限 `0600`。

首次启用浏览器时，Worker 在数据目录生成 `browser-scope` UUID 文件（`0600`）。此身份独立于 Workspace 和负责人，统一用于 Token、Desktop Agent、服务代理及清理。保留该文件可在重启后维持作用域；各 Worker 必须使用独立数据目录。由旧版本升级后浏览器改用新作用域，需要重新建立浏览器会话，此后绑定变化不再改变作用域。

配置浏览器后即创建本地服务代理，无需等待绑定。

Codex Desktop 的浏览器操作通过 `TYRS_HAND_BROWSER_AGENT_ADDRESS` 对应的 Browser Agent 通道完成。Worker Browser 与多个 Desktop Browser 客户端可并发运行；任一 Desktop 客户端断开不影响其他连接。

验收 Browser 时必须同时核对：

- 页面可见动作与工具返回值
- Worker 心跳和 Browser metadata
- Browser MCP/Agent 状态与文件交换
- 已绑定活动的 Task、Tool Call、Projection 和 Outbox 记录；未绑定活动不应产生 Control 上报队列
- 并发链路断开后的隔离性

## 8. 升级与回滚

内部部署版本使用 `deploy-N.A`；开源 Release 使用 SemVer，两条版本线互不混用。

升级顺序：

1. 备份数据库、Control 配置、Worker 配置和当前二进制。
2. 固定新 Control Digest 与 Worker 精确版本。
3. 停止 Worker 和 Control 写入。
4. 执行当前 baseline 所需的数据操作和 `tyrs-hand-admin migrate`。
5. 启动 Control，再启动 Worker。
6. 完成 GitHub、Discord、Desktop、多客户端、出站 SSH 和 Browser 并发验收。

验收完成前保留数据库备份、旧 Control Digest 和旧 Worker 二进制。回滚时停止服务，恢复三者后按原顺序启动。任何回滚都不替换用户 Codex Home。
