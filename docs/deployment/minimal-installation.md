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
TYRS_HAND_IMAGE=ghcr.io/slovx2/tyrs-hand@sha256:<published-digest>
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
| 管理后台 | GitHub App 凭据、Codex Provider、模型与 Agent 配置 |

GitHub Private Key、Webhook Secret 和 Codex API Key 不要放入 `.env`。它们通过管理后台写入，并使用主密钥加密保存。

## 2. 启动

```bash
docker compose -f compose.yaml -f compose.production.yaml up -d postgres redis
docker compose -f compose.yaml -f compose.production.yaml --profile tools run --rm admin migrate
docker compose -f compose.yaml -f compose.production.yaml up -d server worker
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

系统会为新仓库创建最小 `@mention` 规则。拥有 `triage` 及以上权限的用户在 Issue 或 PR 评论中发送 `@<app-slug>` 即可触发。

## 5. 配置 Codex

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

## 6. 验收

```bash
docker compose -f compose.yaml -f compose.production.yaml ps
curl --fail https://agent.example.com/healthz
```

在已安装仓库的 Issue 中评论 `@<app-slug> 检查当前问题并回复结果`，然后在管理后台确认 Webhook、Job、Thread 和 Worktree 状态。
