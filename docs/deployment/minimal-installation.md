# 最小安装指引

Tyrs Hand 由公网 Control 和一台或多台宿主 Worker 组成。Control 可继续使用 Docker；Worker 与 Relay 已合并为单个宿主常驻进程，不再启动或管理开发容器。

## Control

Control 主机需要 Docker Engine、Docker Compose、PostgreSQL、Redis，以及指向 Control 的 HTTPS 域名。

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

反向代理需转发 Control 的全部路径，包括 `/worker/v2/*`。Worker API 只有四类入口：

```text
POST /worker/v2/enroll
POST /worker/v2/sync
POST /worker/v2/rpc
GET|POST /worker/v2/blobs/{id}
```

管理后台仍负责 GitHub App、Discord、任务偏好、Worker 注册和 Placement。Codex Provider、API Key、ChatGPT 登录态、Base URL 与代理不在 Control 配置，也不会下发给 Worker。

## Worker 宿主依赖

支持以下薄二进制：

- Linux amd64、arm64
- macOS amd64、arm64

宿主机必须预先安装：

- Codex CLI `>= 0.145.0`
- Git `>= 2.39.0`
- OpenSSH Client（`ssh`、`scp`、`ssh-agent`）`>= 9.2.0`
- 一个可执行的用户 Shell

SFTP Server、PTY 和 SSH Server 已编译进 Worker 二进制，不需要安装 `sshd`。完整依赖声明见 `deploy/worker/dependencies.json`。Codex、Git、SSH 及用户需要的语言工具链由宿主自行维护，Tyrs Hand 不安装、不升级这些工具。

每个 Worker 绑定一个真实 OS 用户：

- `HOME` 使用该用户 Home。
- `CODEX_HOME` 使用服务启动时的 `CODEX_HOME`，未设置时为 `~/.codex`。
- 固定项目根目录为 `~/tyrs-hand/workspaces`，项目必须是该目录下的一级目录。
- Codex 会话、登录态、配置和 Skill 均直接使用机器已有数据；安装和旧环境迁移不会复制、改写或清理 Codex Home。

## 安装 Worker

先在管理后台创建 Worker，选择角色与并发上限；若该 Worker 承载 Discord 或 Desktop，再为它创建唯一逻辑环境。逻辑环境只保存项目、Forum 和参与者绑定，不创建容器。完成绑定后生成一次性 Enrollment Token。准备 Codex Desktop 客户端公钥文件，每行一把标准 OpenSSH 公钥，可在行尾添加客户端名称。

从与 Release 相同版本的源码制品目录执行：

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

1. 下载对应 OS/架构的 Release tarball 并校验 SHA-256。
2. 安装 `/usr/local/bin/tyrs-hand-worker`。
3. 创建 Worker 状态目录、固定工作区和多客户端授权公钥文件。
4. 写入 `/etc/tyrs-hand/worker.env`。
5. 执行 `tyrs-hand-worker doctor`。
6. Linux 安装 systemd system unit；macOS 安装 LaunchDaemon。

安装脚本会等待最长 30 秒确认注册；成功后自动从 `/etc/tyrs-hand/worker.env` 删除一次性 Enrollment Token。长期凭据写入 Worker 状态目录，权限为 `0600`。若超时，检查服务日志后手动删除 Token。

手动检查：

```bash
sudo -u <os-user> /usr/local/libexec/tyrs-hand-worker-run doctor
```

## 内置 SSH 与多客户端

Worker 默认监听 `:2222`，可通过 `TYRS_HAND_WORKER_SSH_LISTEN_ADDR` 修改。Host Key 自动生成并持久化。授权公钥文件支持多把 key；不允许 `command=` 等公钥选项，也不允许重复 key。

内置 SSH 支持：

- 完整 Shell、远程命令和环境变量白名单
- PTY 与窗口尺寸变化
- SCP 与内置 SFTP
- 多客户端并发连接
- 拦截 `codex app-server proxy` 并接入唯一宿主 App Server Hub
- 拦截 `tyrs-hand-worker browser proxy` 并转发 Browser Bridge

除 `session` 外的 SSH channel 一律拒绝，因此本地、远程和动态端口转发均不可用。Agent 出站 SSH 是另一套能力，仍通过 Control 的受管 SSH 配置下发。

## Codex 配置边界

Worker 启动唯一系统 Codex App Server：

```text
codex app-server --listen unix://<worker-state>/app-server.sock
```

启动时只设置真实 `HOME` 和 `CODEX_HOME`。Control 不注入 Provider、API Key、ChatGPT Auth、Base URL 或 Proxy，也不在 Codex Home 写入 `config.toml`、`auth.json`、`AGENTS.md` 或迁移标记。

Control 中保留的模型、推理强度、Service Tier 和协作模式属于任务偏好；全局 Agent 指令通过 Thread developer instructions 发送，不写入机器 Home。

## Browser Bridge

Browser Bridge 仍在宿主机独立安装。将 `TYRS_HAND_BROWSER_AGENT_RELAY_ADDRESS` 指向 Bridge Registry，默认 `127.0.0.1:8934`。Worker 的 SSH Browser Proxy 和任务 Browser 工具会复用该宿主能力。

## 唯一旧环境迁移

先升级 Control 并执行数据库迁移，再注册新宿主 Worker。默认命令只做 dry-run：

```bash
tyrs-hand-admin worker migrate-legacy \
  --environment-id <old-environment-uuid> \
  --worker-id <new-worker-uuid>
```

确认输出的 `workspaces/<name>` 与已有外部 Thread 数量后执行：

```bash
tyrs-hand-admin worker migrate-legacy \
  --environment-id <old-environment-uuid> \
  --worker-id <new-worker-uuid> \
  --apply
```

命令只迁移 Control 中的 Worker 关联，不迁移 Codex Home 文件。迁移前应确保旧项目已放到新宿主的 `~/tyrs-hand/workspaces/<name>`，且真实 `CODEX_HOME` 中已有需要保留的聊天记录。

## 升级与回滚

Release 为每个平台提供 tarball 与独立 `.sha256`。升级时先迁移 Control，再替换 Worker 二进制并重启。Worker Journal 和 Control Lease 会恢复已领取任务。

回滚时停止服务、恢复上一个 Worker 二进制并重启。不要回滚或替换用户 `CODEX_HOME`。Worker Protocol 版本必须与 Control 一致；v2 不提供 v1 兼容路由。
