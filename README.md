# tyrs-hand

tyrs-hand 是一个面向 GitHub 的自托管 Agent 控制系统。它使用 GitHub App 接收
Issue、Pull Request、Review 与评论事件，为每个逻辑工作项维护独立 Worktree，
并通过 Codex App Server 在同一 Thread 中持续处理后续指令。

## 组件

- `tyrs-hand-server`：管理 API、GitHub App、Webhook、动态工具控制面和 React 管理后台。
- `tyrs-hand-worker`：持久任务租约、Bare Cache、Worktree 和 Codex App Server 进程池。
- `tyrs-hand-admin`：迁移、恢复、主密钥轮换、Codex 登录、诊断和 GC。
- PostgreSQL：唯一权威状态源和 Durable Queue。
- Redis：限流、SSE 通知等可恢复的临时状态。

项目只使用 GitHub App 身份，不需要普通 GitHub 机器账号。每个仓库维护一个 Bare
Cache，每个 Work Item 维护一个长期 Worktree；同一工作项严格串行，不同工作项可由
多个 Worker 并发处理。

## 本地启动

需要 Go `1.26.5`、Node.js `24.14.0`、pnpm `11.14.0`、PostgreSQL 18、Redis 8，
以及 Codex CLI `0.142.5`。

1. 创建配置与本地 Secret：

   ```bash
   cp .env.example .env
   mkdir -p .local/secrets
   printf '%s' 'tyrs_hand' > .local/secrets/postgres_password
   openssl rand -base64 32
   openssl rand -base64 32
   ```

   将两个随机值分别写入 `TYRS_HAND_MASTER_KEY` 和
   `TYRS_HAND_SETUP_TOKEN`。生产环境必须设置 HTTPS 对应的
   `TYRS_HAND_PUBLIC_URL`，并启用 Secure Cookie。

2. 构建并执行显式迁移：

   ```bash
   docker compose build
   docker compose --profile tools run --rm admin migrate
   docker compose up -d postgres redis server worker
   ```

   Server 和 Worker 启动时只检查迁移，不会自行修改生产数据库。

3. 打开 `http://localhost:8080/setup`，用 Setup Token 创建唯一管理员，立即保存
   TOTP Secret 与一次性恢复码。初始化后可以从环境中移除 Setup Token。

4. 在“GitHub App”页面通过 Manifest 创建 App，或手动录入 App ID、Slug、
   Private Key 和 Webhook Secret。安装 App 后，Installation 与 Repository 会从
   已验签的 Webhook 自动同步。

5. 使用共享 OpenAI 账号时执行：

   ```bash
   docker compose --profile tools run --rm admin codex-login
   ```

   使用兼容 API Key 时，可在“系统设置”中配置 Base URL 与 API Key。Secret
   只会加密存储，并写入隔离的 CODEX_HOME，不会返回前端或进入仓库。

## GitHub App 权限

- Metadata：只读。
- Contents、Issues、Pull Requests：读写。
- Actions、Checks：只读。
- 订阅 Installation、Repository、Issues、Issue Comment、Pull Request、Review、
  Review Comment 与 Push。

默认规则只响应 Bot 被 `@mention` 和 App 被请求 Review。其他事件必须由仓库管理员
显式开启。触发者至少需要 `triage` 或规则指定权限；修改非 Agent PR 或执行 Push 前，
系统会再次向 GitHub 查询当前权限，并要求 `write` 以上。

规则分别保存普通工具与 `dangerousActions`。合并、关闭和类似危险能力不会进入默认
工具集，只有逐仓库显式配置后才会下发给 Agent。

## Thread、Skill 与记忆

- 一个 `(Work Item, Agent Profile, Context Version)` 对应一个 Codex Thread。
- 同一 Issue 或 PR 的后续评论 Resume 原 Thread。
- Turn 已开始时到达的新指令会使用 `turn/steer` 合入；否则保留为下一 Turn。
- Provider、Profile、CODEX_HOME 或 Context Version 不一致时会创建新 Thread，并注入
  持久化的 Work Item Summary。
- Session 默认保留 30 天，关闭的 Worktree 默认保留 7 天。
- 仓库 Skill 只从 `.agents/skills/<name>/SKILL.md` 读取。缺失或未被
  `skills/list` 发现时，任务会作为配置错误失败，不会让模型猜测。
- Codex Native Memory 默认关闭。跨 Work Item 不共享可写 Thread。

Codex 默认使用 `workspace-write`、`approvalPolicy=never` 和公网访问。可以在 Agent
Profile 中禁网。GitHub Token 只在 Turn 外部通过临时 AskPass 用于 Fetch/Push，
不会写入 Remote URL、Worktree 或 Codex 环境。

## 开发与测试

```bash
pnpm --dir web install --frozen-lockfile
make generate
make format-check
make lint
make test
make test-race
make test-integration
make test-coverage
make build
```

集成测试使用 Testcontainers 启动 PostgreSQL 18 和 Redis 8。Codex 测试分为两层：

- 脚本化 Fake App Server 验证 JSONL、初始化、并发响应、超时、断线、Resume、Steer、
  Interrupt 和 Dynamic Tool Server Request。
- 固定 Codex `0.142.5` 配合本地 Mock Responses SSE 上游，验证真实 Thread、Turn、
  Skill 与 namespace dynamic tools；测试不会调用真实模型。

前端使用 Vitest、Testing Library、MSW 和 Playwright。OpenAPI 3.1 同时生成 Go Gin
接口与前端 TypeScript 类型，CI 会检查生成结果漂移。

## 运维命令

```text
tyrs-hand-admin migrate
tyrs-hand-admin check
tyrs-hand-admin reset-password <username> <new-password>
tyrs-hand-admin recover-password <username> <recovery-code> <new-password>
tyrs-hand-admin reset-totp <username>
tyrs-hand-admin rotate-master-key <new-master-key-file>
tyrs-hand-admin codex-login
tyrs-hand-admin gc
```

主密钥轮换会在单个数据库事务中重加密全部 Secret。命令成功后，应先更新 Secret
管理系统中的主密钥，再重启 Server 与 Worker。任何线上 `.env` 变更都应由部署系统
备份并保留历史版本。

## 安全与发布

- 密码使用 Argon2id；Secret 使用 AES-256-GCM；Session Cookie 为随机不透明值。
- Webhook 在限制 Body 大小后执行 HMAC-SHA256 常量时间验签，并按 Delivery ID 去重。
- 任务结果必须匹配当前 lease token 与单调递增 epoch，旧 Worker 无法提交。
- 动态工具同时校验 Capability、Installation、Repository、Work Item、工具白名单和
  实时权限；Tool Call 按 `(thread, turn, call)` 幂等。
- Server 与 Worker 容器均以 UID 10001 的非 root 用户运行。
- 发布工作流构建 linux/amd64 与 linux/arm64 GHCR 镜像，附带 SBOM、provenance、
  漏洞扫描和 Cosign keyless 签名。

项目采用 MIT 许可证。
