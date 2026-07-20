# 最小安装指引

适用于一台 Docker 主机、一个公开 HTTPS 域名和 GitHub.com。默认让管理端与 Webhook 共用一个端口。

## 1. 准备配置

要求：Docker Engine、Docker Compose，以及指向部署主机的 HTTPS 域名。

```bash
cp .env.example .env
install -d -m 0700 .local/secrets
openssl rand -base64 32 > .local/secrets/master_key
openssl rand -hex 32 > .local/secrets/postgres_password
chmod 0600 .env .local/secrets/*
```

在 `.env` 中至少修改：

```dotenv
TYRS_HAND_ENV=production
TYRS_HAND_CONTROL_IMAGE=ghcr.io/slovx2/tyrs-hand-control@sha256:<published-digest>
TYRS_HAND_WORKER_IMAGE=ghcr.io/slovx2/tyrs-hand-worker@sha256:<published-digest>
TYRS_HAND_HOST_PORT=8080
TYRS_HAND_PUBLIC_URL=https://agent.example.com
TYRS_HAND_GITHUB_APP_NAME=my-team-tyrs-hand
TYRS_HAND_SETUP_TOKEN=<long-random-value>
TYRS_HAND_COOKIE_SECURE=true
POSTGRES_PASSWORD=<与 .local/secrets/postgres_password 完全一致>
```

GitHub App 名称必须全局唯一；`TYRS_HAND_SETUP_TOKEN` 可用 `openssl rand -hex 32` 生成。

反向代理将 `TYRS_HAND_PUBLIC_URL` 的全部请求转发到 `127.0.0.1:8080`。GitHub Webhook 地址是：

```text
https://agent.example.com/webhooks/github
```

配置边界：

| 配置位置 | 放什么 |
| --- | --- |
| `.env` / Secret 文件 | 镜像、端口、公开 URL、数据库、Redis、Setup Token、主密钥、存储路径 |
| 管理后台 | GitHub App 凭据、Discord Bot 与 Server、Codex Provider、模型与 Agent 配置 |

GitHub Private Key、Webhook Secret 和 Codex API Key 不要放入 `.env`。它们通过管理后台写入，并使用主密钥加密保存。

## 2. 启动

```bash
docker compose -f compose.yaml -f compose.production.yaml up -d postgres redis
docker compose -f compose.yaml -f compose.production.yaml --profile tools run --rm admin migrate
docker compose -f compose.yaml -f compose.production.yaml up -d server worker discord-worker discord
```

打开 `https://agent.example.com/setup`，使用 `TYRS_HAND_SETUP_TOKEN` 创建管理员，并保存 TOTP 和恢复码。

### Discord Dev Worker

生产 Compose 只向 Discord Dev Worker 挂载宿主 Docker Socket；GitHub Worker 不挂载，Agent 的开发容器也不会得到 Socket。部署前在备份 `.env` 后填写：

```dotenv
TYRS_HAND_DOCKER_GID=<stat -c '%g' /var/run/docker.sock 的结果>
```

然后使用生产 Compose 重建 Discord Dev Worker：

```bash
docker compose -f compose.yaml -f compose.production.yaml up -d --force-recreate discord-worker
```

Discord Dev Worker 用 Socket 管理用户级长期开发容器。每位 Discord 用户在一个 Guild 中复用一个容器和 Home；不同 Forum 各有独立仓库 clone。容器空闲 30 分钟后停止，后续任务自动启动。只有显式删除最后一个 Forum 才会删除容器、镜像、Network、Volume 和 Home。

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

系统会为新仓库创建 Slash Command、首行 Mention 和 Label 规则。拥有 `triage` 及以上权限的用户可以在 Issue 或 PR 评论第一行发送 `/tyrs-hand <指令>`，也可以在第一行任意位置输入精确的 `@<app-slug>`，或添加名称为 `tyrs-hand` 的 Label。Mention 会忽略后续行、引用、代码、转义、URL 和用户名后缀；旧版全文 `@mention` 规则默认关闭，只建议在兼容旧工作流时手动创建。

## 5. 配置 Discord（可选）

当前只支持单个启用 Community 的私有 Discord Server。启用前：

1. 在 Discord Developer Portal 创建 Application 和 Bot，开启 `Server Members Intent` 与 `Message Content Intent`。
2. 通过 OAuth2 把 Bot 邀请进目标 Server。首次初始化需要管理 Server、频道、线程和消息，建议只在专用私有 Server 中授予 Administrator。
3. 在管理后台进入“Discord”，填写 Server ID、Application ID、Bot User ID 和 Bot Token，勾选“启用 Discord 常驻服务”并保存。Bot Token 会进入加密 Secret Store，不要写入 `.env`。
4. 先执行“增量初始化”预检，确认没有冲突后再开始初始化。增量模式只创建或校正 Tyrs Hand 管理的资源，不会删除无关频道。

“全新初始化”会删除目标 Server 的全部 Channel 和 Category，并要求输入包含目标 Server ID 的精确确认指令。只应在空白或专用测试 Server 中使用，不得用它重置已经投入使用的正式 Server。

初始化完成后，在 Discord 页面确认 Gateway 为 `connected`，Outbox 和失败数为 `0`。为成员选择仓库并创建开发 Forum；首个 Forum 的仓库必须在默认分支提供 `.devcontainer/Dockerfile`，最终镜像必须声明非 root `USER`。同一成员后续可以为其他仓库创建 Forum，并复用首个 Forum 确定的容器镜像与 Home。

## 6. 配置 Codex

进入管理后台“系统设置”：

- 使用 API Key：填写兼容 API Base URL、API Key、模型和推理级别。
- 使用共享账号：运行以下命令完成 Device Code 登录。

```bash
docker compose -f compose.yaml -f compose.production.yaml --profile tools run --rm admin codex-login
```

仓库规则使用 Skill 时，文件必须位于：

```text
.agents/skills/<skill-name>/SKILL.md
```

## 7. 验收

```bash
docker compose -f compose.yaml -f compose.production.yaml ps
curl --fail https://agent.example.com/healthz
```

确认 PostgreSQL、Redis、Server、GitHub Worker、Discord Dev Worker 和 Discord Gateway 容器健康。在已安装仓库的 Issue 中评论 `@<app-slug> 检查当前问题并回复结果`，然后在管理后台确认 Webhook、Job、Thread 和 Worktree 状态。

启用 Discord 时，还应在开发 Forum 新建 Post，确认平台构建/启动用户级容器、直接使用该 Forum 绑定仓库、持续更新状态并最终回复。再创建第二个仓库 Forum，确认它复用容器与 Home、但使用独立 clone；最后回到管理后台确认 Gateway 仍为 `connected`，Outbox 没有待处理或失败积压。
