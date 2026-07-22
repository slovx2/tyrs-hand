# 最小安装指引

推荐把 Tyrs Hand 部署成一个公网 Control 和一个主动连接的 Pull Worker。Control 需要公开 HTTPS 域名，用于管理后台、GitHub Webhook、Discord Gateway 和 Worker API；Worker 可以放在家庭服务器或闲置电脑上，只需能够主动访问 Control，不需要家庭公网 IP、端口转发、SSH 反向隧道或 Cloudflare。

Control 和 Worker 也可以位于同一台物理机，但仍应分别使用 Control Compose 与 Worker Compose，保持凭据和运行边界一致。

## 1. 准备 Control

Control 主机需要 Docker Engine、Docker Compose，以及指向它的公开 HTTPS 域名。

```bash
cp .env.example .env
install -d -m 0700 .local/secrets
openssl rand -base64 32 > .local/secrets/master_key
openssl rand -hex 32 > .local/secrets/postgres_password
chmod 0600 .env .local/secrets/*
```

在 Control 的 `.env` 中至少修改：

```dotenv
TYRS_HAND_ENV=production
TYRS_HAND_CONTROL_IMAGE=ghcr.io/slovx2/tyrs-hand-control@sha256:<control-digest>
TYRS_HAND_WORKER_IMAGE=ghcr.io/slovx2/tyrs-hand-worker@sha256:<worker-digest>
TYRS_HAND_HOST_PORT=8080
TYRS_HAND_PUBLIC_URL=https://agent.example.com
TYRS_HAND_GITHUB_APP_NAME=my-team-tyrs-hand
TYRS_HAND_SETUP_TOKEN=<long-random-value>
TYRS_HAND_COOKIE_SECURE=true
POSTGRES_PASSWORD=<与 .local/secrets/postgres_password 完全一致>
```

GitHub App 名称必须全局唯一；`TYRS_HAND_SETUP_TOKEN` 可用 `openssl rand -hex 32` 生成。Control 保留配对的 Worker Digest，便于发布审计与回滚，但不会在本机启动 Worker。

反向代理应把该域名的全部请求转发到 `127.0.0.1:8080`。主要入口包括：

```text
https://agent.example.com/webhooks/github
https://agent.example.com/worker/v1/*
```

Cloudflare 是可选项。只要域名能够通过标准 HTTPS 访问，Worker 就可以直接连接 Control。

配置边界：

| 配置位置 | 放什么 |
| --- | --- |
| Control `.env` / Secret 文件 | 镜像、端口、公开 URL、数据库、Redis、Setup Token、主密钥、附件路径、Worker API 网络策略 |
| 管理后台 | GitHub App、Discord Bot、Codex Provider、模型、Agent 配置、执行节点和默认 Placement |
| Worker `.env` / 数据卷 | Control URL、节点标识、角色、容量、一次性注册 Token、长期节点凭据和本地执行数据 |

GitHub Private Key、Webhook Secret、Discord Bot Token 和 Codex API Key 不要放入 Worker。它们由 Control 加密保存，并按 Run 最小权限提供所需能力。

## 2. 启动 Control

```bash
docker compose -f compose.yaml -f compose.production.yaml up -d postgres redis
docker compose -f compose.yaml -f compose.production.yaml --profile tools run --rm admin migrate
docker compose -f compose.yaml -f compose.production.yaml up -d server discord
```

打开 `https://agent.example.com/setup`，使用 `TYRS_HAND_SETUP_TOKEN` 创建管理员，并保存 TOTP 和恢复码。

## 3. 注册 GitHub App

推荐使用 Manifest：

1. 登录管理后台，进入“GitHub App”。
2. 点击“通过 Manifest 创建 GitHub App”。
3. 在 GitHub 完成创建并返回管理后台。App ID、Private Key 和 Webhook Secret 会自动保存。
4. 保持 App 为 **Private / Only on this account**。当前自托管模式不应开放给其他账号安装。

需要手动注册时，在 GitHub 的 `Settings -> Developer settings -> GitHub Apps -> New GitHub App` 配置：

- Homepage URL：`TYRS_HAND_PUBLIC_URL`
- Webhook URL：`TYRS_HAND_PUBLIC_URL/webhooks/github`
- Webhook：Active，并生成随机 Secret
- Repository permissions：Metadata `Read`；Contents、Issues、Pull requests `Read and write`；Actions、Checks `Read`
- Subscribe to events：Repository、Issues、Issue comment、Pull request、Pull request review、Pull request review comment、Push
- Where can this GitHub App be installed：Only on this account

创建后生成 Private Key，在管理后台“GitHub App -> 手动配置”填写 App ID、Client ID、App Slug、Private Key PEM 和 Webhook Secret。

## 4. 安装到仓库

1. 打开 `https://github.com/apps/<app-slug>/installations/new`。
2. 选择账号，并只授权需要接入的仓库。
3. 回到管理后台，确认 Installation 和 Repository 已同步。

系统会为新仓库创建 Slash Command、首行 Mention 和 Label 规则。拥有 `triage` 及以上权限的用户可以在 Issue 或 PR 评论第一行发送 `/tyrs-hand <指令>`，也可以输入精确的 `@<app-slug>`，或添加名称为 `tyrs-hand` 的 Label。

## 5. 配置 Discord（可选）

当前只支持单个启用 Community 的私有 Discord Server。启用前：

1. 在 Discord Developer Portal 创建 Application 和 Bot，开启 `Server Members Intent` 与 `Message Content Intent`。
2. 通过 OAuth2 把 Bot 邀请进目标 Server。首次初始化建议只在专用私有 Server 中授予 Administrator。
3. 在管理后台进入“Discord”，填写 Server ID、Application ID、Bot User ID 和 Bot Token，勾选“启用 Discord 常驻服务”并保存。
4. 先执行“增量初始化”预检，确认没有冲突后再开始初始化。

“全新初始化”会删除目标 Server 的全部 Channel 和 Category，只应在空白或专用测试 Server 中使用。初始化完成后，确认 Gateway 为 `connected`，Outbox 和失败数为 `0`。

## 6. 配置 Codex

进入管理后台“系统设置”，配置 API Key Provider、兼容 API Base URL、模型和推理级别。分布式 Worker 第一版不支持 Device Code；Provider Key 留在 Control，由 Run 限定的凭据接口下发，不要写入 Worker `.env`。

同一页面还可以编辑“全局 AGENTS.md”。保存后，Control 会把内容注入所有新建或再次使用的 Codex Home，包括 GitHub Worker Home 和 Discord 开发容器 Home。该文件适合放组织级通用规则；仓库自己的 `AGENTS.md` 仍可补充仓库级约束。修改不会改写仓库文件。

仓库规则使用 Skill 时，文件必须位于：

```text
.agents/skills/<skill-name>/SKILL.md
```

## 7. 创建执行节点

在管理后台“执行节点”页面：

1. 创建节点，选择 `github`、`discord` 角色和并发容量。
2. 生成短期、单次使用的 Enrollment Token。
3. 把 GitHub 和 Discord 默认执行节点都设为该节点。

默认节点只影响新资源。Discord 开发环境和 GitHub Work Item 创建后会冻结实际节点；修改默认值不会迁移已有环境、Work Item 或 Codex Thread。第一版不要求一个 Discord 用户绑定一个节点。

未设置 GitHub 默认节点时，Intent 会进入 `placement_pending`；未设置 Discord 默认节点时，创建开发环境会返回明确配置错误。节点离线或禁用时，已有任务继续等待原节点，不会自动漂移。

## 8. 启动 Pull Worker

把 `compose.worker.yaml` 和单独的 `.env` 放到 Worker 主机。Worker 主机至少需要 Docker Engine、Docker Compose 和 OpenSSH Client；启用 Discord 开发容器时，开发镜像也必须包含 `ssh`、`scp` 和 `sftp`。先查询 Docker Socket GID：

```bash
stat -c '%g' /var/run/docker.sock
```

Worker `.env` 至少包含：

```dotenv
TYRS_HAND_WORKER_IMAGE=ghcr.io/slovx2/tyrs-hand-worker@sha256:<worker-digest>
TYRS_HAND_WORKER_CONTROL_URL=https://agent.example.com
TYRS_HAND_WORKER_ID=home-1
TYRS_HAND_WORKER_ROLE=all
TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS=6
TYRS_HAND_WORKER_ENROLLMENT_TOKEN=<一次性 Token>
TYRS_HAND_ENABLE_DEVELOPMENT_CONTAINERS=true
TYRS_HAND_DOCKER_GID=<Docker Socket GID>
TYRS_HAND_ENABLE_SSH=true
TYRS_HAND_BROWSER_MCP_URL=http://host.docker.internal:8931/mcp
```

不启用某项能力时，分别把 `TYRS_HAND_ENABLE_SSH` 设为 `false`，或把 `TYRS_HAND_BROWSER_MCP_URL` 留空。浏览器 MCP Token 不写入 `.env`，而是通过只读文件挂载提供。

当前 `compose.worker.yaml` 还包含这些能力所需的宿主集成，部署时不要遗漏：

- 把 `/opt/tyrs-hand/ssh-agent` 挂载到 Worker 的 `/run/tyrs-hand-ssh-agent`。
- 把 `/opt/tyrs-hand/browser-files` 挂载到 `/run/tyrs-hand-browser-files`。
- 把 `/opt/tyrs-hand/browser/browser_mcp_token` 只读挂载到 `/run/tyrs-hand-browser/browser_mcp_token`。
- 增加 `host.docker.internal:host-gateway`，让 Worker 和受管开发容器访问宿主 Bridge。

SSH 尚未启用、浏览器宿主尚未安装时，先创建 Compose bind mount 的来源，避免容器启动时由 Docker 自动创建错误属主的目录或空目录：

```bash
sudo install -d -o 10001 -g 10001 -m 0755 /opt/tyrs-hand/ssh-agent
sudo install -d -m 0777 /opt/tyrs-hand/browser-files
sudo install -d -m 0750 /opt/tyrs-hand/browser
sudo sh -c 'test -e /opt/tyrs-hand/browser/browser_mcp_token || install -m 0444 /dev/null /opt/tyrs-hand/browser/browser_mcp_token'
```

最后一行只创建尚未启用浏览器时所需的空占位文件；宿主安装脚本会把它替换成随机 Token。如果已经安装 Browser Bridge，不要覆盖现有 Token。

启动并等待长期节点凭据生成：

```bash
docker compose -f compose.worker.yaml up -d worker
docker compose -f compose.worker.yaml ps
```

长期凭据位于 Worker 数据卷的 `control-state/node-credential`，权限应为 `0600`。注册成功后必须从 `.env` 清空 `TYRS_HAND_WORKER_ENROLLMENT_TOKEN`，再强制重建以确认 Worker 只依赖长期凭据：

```bash
docker compose -f compose.worker.yaml up -d --force-recreate worker
```

Worker 不应配置 PostgreSQL、Redis、Control 主密钥、Discord Bot Token 或 Provider API Key；运行所需凭据由 Control 按 Run 限定下发。Discord 角色需要 Docker Socket 来管理开发容器；只承担 GitHub 角色的节点可以移除该挂载并关闭开发容器能力。

## 9. 配置受管 SSH

SSH 使用 Worker 内的标准 `ssh-agent` 和 OpenSSH CLI，不需要 SSH MCP。私钥由 Control 加密保存，只在配置同步时通过 HTTPS 下发给被分配的节点，并直接载入 Agent；私钥不会写入 Worker、开发容器或工作区文件系统。

在管理后台“SSH”页面手工完成配置：

1. 添加凭证，填写名称、私钥和可选的私钥口令。页面只会显示公钥指纹、版本、启用状态和关联主机数，不会回显私钥或口令。
2. 添加主机，填写别名、HostName、端口、用户名和凭证；需要跳板机时选择 ProxyJump。
3. 把主机分配到对应 Execution Node。ProxyJump 主机必须同时分配到该节点。
4. 在“执行节点”页面确认 SSH 状态为 `ready`，并核对主机数与凭证数。

凭证轮换时编辑现有凭证并粘贴新私钥；私钥留空表示保留现值。仍有关联主机的凭证不能删除。Worker 每 60 秒按 ETag 检查配置，完整加载新配置后才原子切换 `current.sock`，旧 Agent 会短暂保留以完成正在运行的命令。

Agent 使用生成的 SSH Config，支持 `ssh`、`scp`、`sftp`、PTY、ProxyJump 和端口转发。本期不限制目标、远端命令、目录、超时或转发；需要限制时应优先在远端账号、`authorized_keys` forced-command 或 `sudo` 策略中实现。

## 10. 安装宿主 Chrome Bridge

这项能力复用 Worker Ubuntu 桌面用户当前 Chrome Profile、登录态和全部可调试的普通标签页。它不启用 Remote Debugging，也不需要每次连接时人工确认。`chrome://`、扩展页等 Chrome 不允许调试的页面不会被接管。

宿主机需要：

- Google Chrome，并由目标桌面用户正常登录图形会话。
- `/usr/local/bin/node` 可执行文件；Bridge 的依赖和版本由发布制品锁定。
- `systemd --user` 和该用户的 D-Bus，`/run/user/<uid>/bus` 必须存在。
- `setfacl`；Ubuntu 可通过系统包 `acl` 提供。
- Docker/Compose，以及未向 LAN 或 Tailscale 发布的宿主端口 `8931`、`8932` 和内部端口 `8933`。

在 Tyrs Hand 源码根目录执行：

```bash
test -x /usr/local/bin/node
command -v google-chrome
command -v setfacl
test -S "/run/user/$(id -u <desktop-user>)/bus"
sudo deploy/browser/install-host-release.sh <desktop-user>
```

安装脚本读取 `deploy/browser/browser-artifacts.lock.json`，并自动完成：

- 按精确 URL 和 SHA256 下载 CRX、Playwright Core 与 Bridge bundle。
- 生成两枚独立随机 Token，分别用于 Extension Relay 和 MCP HTTP。
- 创建 `/opt/tyrs-hand/browser-files`、版本目录和最小 ACL，并原子切换 `releases/current`。
- 写入 Chrome 企业策略 `/etc/opt/chrome/policies/managed/tyrs-browser.json`。
- 为桌面用户安装并启动 `tyrs-browser-bridge.service`。

Token 不需要也不应手工复制到 Chrome 或 Worker `.env`。当前发布的扩展 ID 记录在制品锁中；`tyrs-v0.1.0` 对应 `ljjpfmlebedjianbadehibibioaknkfb`。

安装脚本完成后，仍需桌面用户手工让 Chrome 加载策略：

1. 完全退出 Chrome 的所有窗口和进程，再重新启动。也可以先打开 `chrome://policy`，点击 **Reload policies**；如果 Chrome 提示需要重启，仍应完整退出后重开。
2. 在 `chrome://policy` 确认 `ExtensionSettings` 包含制品锁中的扩展 ID。
3. 打开 `chrome://extensions`，确认 **Tyrs Browser Extension** 已启用并显示“由贵组织管理”。
4. 不要给 Chrome 增加 Remote Debugging 参数，也不要手工填写 Token。

验证 Bridge：

```bash
systemctl --user --no-pager status tyrs-browser-bridge.service
curl --fail http://127.0.0.1:8931/health
```

扩展尚未连接时 `/health` 返回 `degraded`；加载成功后应返回 `ready`、扩展与 Chrome 版本、Profile 和标签页数量。Bridge 的 MCP 端点必须携带 Bearer Token，未授权请求返回 `401` 是预期结果。

Bridge 只允许 loopback 和 Docker bridge CIDR，防火墙也不应向 LAN/Tailscale 开放这些端口。Agent 验证开发服务时，服务必须监听 `0.0.0.0`；平台会把 Worker 或当前开发容器的端口解析成宿主 Chrome 可访问的地址。

浏览器上传和下载使用 `/opt/tyrs-hand/browser-files` 交换目录。工作区文件需要先通过平台工具暂存，单文件上限为 25 MiB；符号链接和工作区越界路径会被拒绝。任务结束时会清理文件，Sweeper 也会删除超过 1 小时的残留。

## 11. 可选的 Worker API IP 白名单

Control 可限制能够访问 `/worker/v1` 的 Worker 出口地址：

```dotenv
TYRS_HAND_WORKER_API_IP_ALLOWLIST=203.0.113.8,198.51.100.0/24,2001:db8::/48
TYRS_HAND_WORKER_API_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

白名单支持单个 IPv4/IPv6 和 CIDR，逗号分隔；留空表示不限制。直连时使用 TCP 来源地址；经过反向代理时，只有代理上一跳位于 `TYRS_HAND_WORKER_API_TRUSTED_PROXIES` 才采信转发头。Cloudflare 存在时可以使用其来源头，但不是启用白名单的前提，也不要求改造为 Cloudflare 架构。

## 12. 更新与回滚

发布新的浏览器 fork 时，先用精确 Release、提交和 SHA256 更新 `deploy/browser/browser-artifacts.lock.json`，再重新运行：

```bash
sudo deploy/browser/install-host-release.sh <desktop-user>
```

脚本会把制品安装到独立版本目录，校验成功后才切换 `releases/current`。安装完成后以桌面用户执行 `systemctl --user restart tyrs-browser-bridge.service`，让正在运行的 Bridge 使用新版本。旧版本目录可保留用于回滚；回滚时把 `current` 原子指向已验证的旧版本，并重启该服务。如果扩展 ID 或策略变化，还需要重新加载 Chrome Policy 并重启 Chrome。

Control 和 Worker 回滚必须使用同一次内部发布的成对 Digest。能力也可以独立关闭：

```dotenv
TYRS_HAND_ENABLE_SSH=false
TYRS_HAND_BROWSER_MCP_URL=
```

修改 Worker `.env` 前先创建时间戳备份并只保留最近 4 份，然后重建 Worker。关闭能力不会删除已保存的 SSH 配置、浏览器 Release 或交换目录。

## 13. 验收

Control 主机：

```bash
docker compose -f compose.yaml -f compose.production.yaml ps
curl --fail https://agent.example.com/healthz
curl --fail https://agent.example.com/readyz
```

Worker 主机：

```bash
docker compose -f compose.worker.yaml ps
docker compose -f compose.worker.yaml logs --no-color --tail=200 worker
curl --fail http://127.0.0.1:8931/health
```

确认 PostgreSQL、Redis、Server 和 Discord 健康；执行节点为 `online` 且持续更新心跳；GitHub、Discord 默认节点正确；SSH 为 `ready`；Chrome 扩展加载后浏览器状态为 `ready`；Worker 日志没有认证、协议、配置同步或租约循环错误。

最后分别从真实 GitHub Issue/PR 和 Discord Forum 启动一次任务，在两条链路中各执行一次 SSH 操作和一次宿主 Chrome 前端验证。确认任务冻结并运行在预期节点，Chrome 能枚举和控制当前 Profile 的普通标签页且全程无确认弹窗，Gateway、Projection 与 Outbox 没有积压。未录入 SSH 凭证或未完成 Chrome 策略加载时，只能把对应项记为“未覆盖”，不能仅凭健康接口宣称完整 E2E 通过。
