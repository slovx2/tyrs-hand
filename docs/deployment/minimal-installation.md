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

把 `compose.worker.yaml` 和单独的 `.env` 放到 Worker 主机。先查询 Docker Socket GID：

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
```

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

## 9. 可选的 Worker API IP 白名单

Control 可限制能够访问 `/worker/v1` 的 Worker 出口地址：

```dotenv
TYRS_HAND_WORKER_API_IP_ALLOWLIST=203.0.113.8,198.51.100.0/24,2001:db8::/48
TYRS_HAND_WORKER_API_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

白名单支持单个 IPv4/IPv6 和 CIDR，逗号分隔；留空表示不限制。直连时使用 TCP 来源地址；经过反向代理时，只有代理上一跳位于 `TYRS_HAND_WORKER_API_TRUSTED_PROXIES` 才采信转发头。Cloudflare 存在时可以使用其来源头，但不是启用白名单的前提，也不要求改造为 Cloudflare 架构。

## 10. 验收

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
```

确认 PostgreSQL、Redis、Server 和 Discord 健康；执行节点为 `online` 且持续更新心跳；GitHub、Discord 默认节点正确；Worker 日志没有认证、协议或租约循环错误。再分别执行一次 GitHub Issue/PR 和 Discord Forum 真实任务，确认任务都冻结并运行在预期节点，Gateway、Projection 与 Outbox 没有积压。
