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

- Codex CLI `0.147.0`
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

Provider、API Key、ChatGPT Auth、Base URL 与 Proxy 只配置在机器用户自己的 Codex Home 中。Control 不写入 `config.toml`、`auth.json` 或 `AGENTS.md`。

若机器 Codex Home 的 Provider 通过环境变量引用 API Key，可由宿主管理员将对应变量写入可选的 `/etc/tyrs-hand/codex.env`。Linux systemd Worker 和 `tyrs-hand-worker-run` 会加载该文件，但安装器不会创建、改写或备份它；文件应由 `root:<worker-group>` 持有并设置为 `0640`。这些变量仍属于机器 Codex 配置，不进入 Control、Worker 协议或任务快照。

Worker 启动时读取一次机器 Codex Home 和 `codex.env`，并为整个 Worker 生命周期启动唯一 Codex App Server。配置变更不热加载；修改后应手动重启 Worker，所有 Desktop 客户端随后重新连接同一个新实例。

Control 的 GitHub Agent Profile、仓库覆盖和全局 GitHub Agent instructions 只作用于 GitHub Work Item；Desktop、Discord 与 Mobile 的未指定参数由机器 Codex Home 决定。

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

## 7. Browser

Worker 任务直接访问宿主 Browser MCP 和宿主文件目录。Token 仅从 `TYRS_HAND_BROWSER_MCP_TOKEN_FILE` 读取，文件应为 Worker 用户所有且权限 `0600`。

Codex Desktop 的浏览器操作通过 `TYRS_HAND_BROWSER_AGENT_ADDRESS` 对应的 Browser Agent 通道完成。Worker Browser 与多个 Desktop Browser 客户端可并发运行；任一 Desktop 客户端断开不影响其他连接。

验收 Browser 时必须同时核对：

- 页面可见动作与工具返回值
- Worker 心跳和 Browser metadata
- Browser MCP/Agent 状态与文件交换
- Task、Tool Call、Projection 和 Outbox 记录
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
