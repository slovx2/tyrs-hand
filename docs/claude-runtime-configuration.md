# Claude 运行时配置

控制台的 Worker「运行时配置」页分别管理 Codex 和 Claude Code。Claude 使用原生
`CLAUDE.md` 与 `settings.json`，格式遵循 [Claude 配置目录文档](https://code.claude.com/docs/en/claude-directory)。

Worker 的 Claude 配置目录为：

```text
<WorkerDataRoot>/claude-code/config/claude/
  CLAUDE.md
  settings.json
  settings.json.bak.1 … settings.json.bak.4
```

该目录作为 Claude runtime 的 `CLAUDE_CONFIG_DIR`，对应原生默认的 `~/.claude`。
不会改写运行 Worker 的用户个人配置目录，也不复制 Codex 的 AGENTS.md 或登录态。
全局 `CLAUDE.md` 对该 runtime 下的项目生效；项目自己的指令文件继续按原生规则加载。

Provider 示例（虚拟凭据）：

```json
{
  "model": "claude-sonnet-4-6",
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:4321",
    "ANTHROPIC_AUTH_TOKEN": "example-only"
  }
}
```

控制台支持 API Key (`ANTHROPIC_API_KEY`，x-api-key) 和 Auth Token
(`ANTHROPIC_AUTH_TOKEN`，Bearer)。切换认证方式需填入对应凭据，保存时移除另一种认证字段。
密钥输入留空保留当前方式的密钥，清除操作显式删除。响应不包含密钥或任意原始设置。
现有 hooks、权限和其他 settings 字段会保留。保存模型时清理旧的 `ANTHROPIC_MODEL`，
避免它继续覆盖 `model`；默认模型留空则使用 Claude 原生默认值。

Claude 专用环境文件只允许运行开关，不接受 Provider 字段；宿主模型环境变量不继承。
客户端未指定模型时，适配器的 `claude-default` 将选择交给 SDK，从而遵循原生配置。
显式会话模型仍优先于全局默认模型。原生项目设置及受管理设置仍遵循 Claude 自身优先级。

配置 API 为 `/workers/{id}/runtimes/{engine}/config`，引擎仅允许 `codex` 与
`claude-code`。控制通道 RPC 必须携带同一引擎，缺省和未知值直接拒绝。
每次修改携带读取时的 revision；并发或过期修改返回冲突，不覆盖新版本。
Claude 文件原子写入，权限 0600，修改前保留四份历史版本。备份失败则不覆盖现配置。

验证包含配置真实 WebSocket 通道、原生文件与备份检查、并发冲突及控制台交互测试。
`make test-runtime-e2e` 使用真实双 SSH、Hub、SDK/CLI 与本地 Mock LLM，断言
settings 中的认证、地址、模型以及 CLAUDE.md 实际进入请求。

入口配置使用 `TYRS_HAND_WORKER_CLAUDE_ENABLED=true` 显式启用 Claude，
`TYRS_HAND_WORKER_CLAUDE_SSH_LISTEN_ADDR` 默认 `:3333`，Codex 保留 `:2222`。
两个入口读取同一份客户端授权公钥，Host Key、状态目录、进程和 Controller 独立。
两引擎和任务调度共用 Worker 并发配额。
授权文件变更在一秒内同步到两个入口，并关闭被撤销密钥的既有连接。
授权文件丢失、不可读或损坏时撤销全部授权；修复文件后自动恢复，不重启引擎。

Control 的 `worker_runtimes` 以 `(worker_id, engine)` 保存完整心跳快照。
运行状态、指纹、构建版本和模型目录独立保存；未出现在新快照中的引擎停用但不删除。
任一指纹变化或冲突会回滚整次心跳。旧 Worker 数据一次性回填为 Codex，
新协议拒绝缺失运行时快照的心跳。控制台概览通过 `/workers/{id}/runtimes`
展示各入口；心跳超过两分钟显示离线。
所有已认证 Worker 请求（含 WebSocket 握手和文件传输）必须携带
`X-Tyrs-Worker-Protocol: 33`，缺失或版本不符返回 409。数据库迁移更新期望版本
不会授权仍在运行的旧 Worker；协调升级完成后才恢复任务派发。

`TestWorkerBootstrapRealSSHSharedBudgetAndGitTool` 使用正式 Worker 启动流程，
验证真实 SSH、SDK→Hub→Git commit 的文件副作用、工具结果回到下一次模型请求，
以及两个引擎的并发限制。该用例加入 `make test-runtime-e2e`，输出独立 JUnit、
wire trace、模型请求和 Git 副作用证据。
每轮证据保存在 `.artifacts/protocol/runs/<runId>/`，`latest.json` 指向最近一轮及
其验收范围。只运行入口验收不会沿用上一轮完整矩阵的覆盖报告。

Control 会话、桌面创建请求和定时任务现已保存不可变的 `engine`；迁移 030/031
将旧记录归入 Codex，并迁移桌面提交去重键，保留原 Intent 与消息的关联。
Thread 索引使用 `(worker_id, engine, external_thread_id)`，相同项目与相同 thread ID
可分别属于两引擎。创建、fork、元数据、命名、归档及标题任务均使用运行时作用域。
Worker 会话操作请求必须携带 `X-Tyrs-Runtime-Engine`；缺失或未知值返回 400。
Claude 的会话、Run、事件、附件、工具及输入决议接口使用该作用域。
未开放的 Worker 全局管理或专属接口返回 501，不将请求送入 Codex。
定时任务从来源 Session 继承引擎；列表、更新、删除和立即运行使用相同作用域。
调度器重建后仍从任务记录创建同引擎 Session 与 Control。Claude 标题请求使用
`claude-default` 遵循原生模型配置，不再指定 GPT、Codex fast tier 或其 effort。

Worker 升级在持有数据锁时一次性迁移旧 Codex Journal，保存原文件备份并保留未确认事件。
迁移完成后正常恢复严格要求引擎；不再为缺失字段的 Journal 推断默认值。
`make test-control-runtime` 验证真实 HTTP/数据库的 ID 冲突、跨引擎操作拒绝、
元数据、生命周期、标题任务、调度继承和升级；必需用例缺失或 skip 会失败。
报告、JUnit 与测试日志在 `.artifacts/control-runtime/<runId>/`，CI 独立保存证据。
这些是 Control 数据层验收，不计入 SSH→SDK→Mock LLM 的协议覆盖率。

迁移 032 将工具调用与交互提问的协议 ID 去重限定在 Control 内，并用复合外键
校验 Run、Intent 与 Control 的归属。历史工具结果与已回答提问保持原样。
同引擎的工具重试返回原结果，改变参数会被拒绝；两引擎相同 ID 分别执行。
交互提问校验原生进程 generation、request ID 和问题内容，多端竞争只接受一个答案。
Control 必需用例包含实际定时任务记录、重复工具提交及交互回答仲裁断言。

单一 Runner 轮流领取两个运行时队列，按返回快照校验引擎后交给对应执行器。
执行器使用独立客户端、Journal 和会话协调器，并共享 Worker 并发预算。
恢复前检查所有 Journal 的目录归属；已有终态只补报，不再执行模型或工具。
Control 登记或终态确认失败时，已接受的输入在进程内持续去重；超过补报预算或被永久
拒绝时保留带 `controlReportStopped` 的 Journal，重启后禁止自动重放，等待明确对账。
此状态尚未提供自动对账恢复入口，不能将保留 Journal 视为 Control 已确认成功。
启用 Claude 时，其 Controller 与 Control 同步也启用。Live 的绑定、可选会话列表
和跨会话工具限定 Codex；不能从 Live 转入 Claude 会话。

`make test-control-runtime-e2e` 启动临时 PostgreSQL 与 Redis，经 Unix socket 代理
连接在隔离网络中运行的真实 Control 和 Worker。真实 SSH 客户端分别提交两引擎任务；
Claude SDK 调用平台工具创建 heartbeat，Worker 重启后调度器恢复同一原生会话。
断言包含实际调度记录、工具结果进入模型上下文、追加的用户消息、Worker ID 不变和
Codex 未收到 Claude 任务。脚本固定镜像 digest，保存本轮 JUnit、wire、模型请求及
数据库断言证据，并清理本轮临时容器。该用例也纳入完整协议矩阵与 Ubuntu CI。

当前发布状态：`releaseReady=false`。Control 数据层已覆盖领取、事件幂等与终态隔离；
在线 Control→真实双运行时的正向链路及重启后调度已验证，仍需补齐全部故障路径、手机运行时关联、Discord 引擎
选择和完整协议矩阵。未启用的 runtime 重启会明确报错。
这部分验收通过不代表完整双引擎协议矩阵或移动/桌面 GUI 发布验收通过。
