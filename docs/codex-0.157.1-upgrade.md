# Codex 0.157.1 升级进度

更新时间：2026-09-28。Phase 1，以及 Phase 2 的回退、stdin 审批和扩展表单链路已完成；升级整体验收尚未完成，`releaseReady=false`，不能据此发布生产。

## 最新接续：门禁分类收口与剩余上游缺口（2026-09-29）

本节优先于下方所有历史状态。验证仍以本地 Linux 为主（见下一节运行器），推送前先跑通本地完整常规 CI（`.local/linux-matrix/ci-local/run.sh`）。

### 用户本轮决定（2026-09-29）

- **不适用的服务端消息不做门禁**：通知与服务端请求由运行时单向下发，某引擎从不产生时无法取得 -32004 证据。登记为 `not-applicable`、写明原因并挂占位用例 `NA-SERVER-MESSAGE` 后直接通过。占位的自洽校验见 `tools/protocol-inventory/not-applicable-server-messages.test.mjs`。客户端请求的不适用仍须真实 -32004 证据。
- **明确不支持、去掉门禁**：新增分类 `unsupported`，挂占位用例 `UNSUPPORTED-NO-GATE` 并写明原因。清单须与 `tools/protocol-inventory/unsupported-capabilities.test.mjs` 的 `decided` 完全一致，新增项必须先取得用户决定。目前均为 Codex 侧：
  - ChatGPT 登录态方法：用量、限额、工作区消息、重置额度、充值提醒；
  - 插件分享与远端插件技能，以及 provider 认证恢复通知；
  - realtime 语音条目；
  - `feedback/upload`；
  - Touch ID 用户验证；
  - 插件市场的 add/remove/upgrade；
  - Gateway OAuth 登录及其状态通知；
  - Guardian 放行与自动审批复核。
  运行时行为不变，Hub 仍原样透传给 Codex。

### 本轮修复

- **Hub 吞掉临时会话的 `thread/closed`**：最后一个客户端退订临时会话时，Hub 会把退订转给上游，但上游随后发来的 `thread/closed` 已没有订阅者。现在该通知转交给发起退订的会话（f0efbca）。CAPABILITY-003 已补 `thread/closed`、`account/login/cancel`、`account/bedrock/setup` 的真实证据。
- **Discord 回复与元数据补报死锁**：锁序统一为先 Session 后 Conversation（486da59），有确定性复现专项。
- **测试竞态**（均为测试问题，非产品缺陷）：
  - `TestRunnerDispatchesBothEnginesOnceWithSharedBudget`：改为等 Journal 删除后再停止；此前 race 下约 1/100 失败，现在 200 次通过。
  - ISOLATION-002：Codex 0.157.1 在回合进行中轮询 `thread/read(includeTurns)` 会出现两种情况：
    - 会话元数据尚未落入状态库，返回 `-32601 list_turns is not supported yet`；
    - 持久历史仍为 inProgress 而线程已不活跃时，回合被规整为 `interrupted`。
    改为以 `turn/completed` 事件为准，本地 20/20 通过。其余用 `waitSessionTurn` 轮询 Codex 的专项如出现同类偶发，按同法处理。

  - `waitSessionTurn` 同时加固：`list_turns` 瞬态重试；`interrupted` 持续 2 秒才判定失败（真实中断不会再变回完成）。
  - FAILURE-007：Worker 发往 Codex 的周期 `model/list` 恰在故意 SIGKILL 时未获响应，被判为“遗漏客户端请求”。现在仅当连接属于被杀进程树时，记为截断证据 `processTerminatedRequests`；其他遗漏仍然失败。
- **CAPABILITY-001 漏测一项**：适配器已拒绝 `account/workspaceMessages/read`，但测试夹具未发送该请求。已补齐（适配器 14c592c，主库 pin 同步），登记清单与夹具现已一一对应。

### 最后 6 项缺口的根因与收口（全部不是原生报文缺陷）

逐项查阅 Codex 0.157.1、0.158.0、0.160.0-alpha.2 及上游 main 源码后确认：

- **timeline 的 3 项 schema 差异**：Rust 类型用 serde `rename_all_fields = "camelCase"` 序列化，schemars 生成 JSON Schema 时未应用该规则，写成了 `turn_id`/`started_at` 等下划线字段。同一 CLI 生成的官方 TS 定义与原生 wire 都是驼峰。
  - 新增 `protocol/schema-corrections/0.157.1.json`，只允许字段改名，且必须命中原字段，否则直接报错。
  - `tools/protocol-inventory/schema-corrections.test.mjs` 核对勘误后的字段集与官方 TS 完全一致。
  - 扩展机制仍禁止覆盖原生 schema。
- **REVIEW-006 / `review/start@codex`**：
  - 原生 inline 审查直接提交 `Op::Review`，审查任务不调用 `emit_turn_started`，开始信号是 `review/start` 响应中的 inProgress 回合。现断言原生不发 `turn/started`，Hub 也不补造。
  - 0.157.1 的会话默认为分页历史，原生明确拒绝分页会话的 detached 审查（detached 也已弃用）。现断言拒绝且无副作用，并按弃用说明的替代路径（新建会话后做 inline 审查）验证独立审查会话。
  - 重启后原生按审查回合的持久上下文恢复只读无网络沙箱（只会收窄）。
  - 分页会话的 inline 审查会另留一个装审查提示的 interrupted 回合。以重启前后逐项一致对账，不写死回合数。
- **MCP-017**：在稳定的 Legacy MCP 协议模式下，原生有意丢弃 `tools/list` 的游标，只有 UnderDevelopment 且默认关闭的 `mcp_2026_07_28` 才跟随分页。现精确断言原生只请求首页、Hub 如实呈现首页工具；resources 仍须完整分页。上游一旦改变行为，用例会报出。
- **0.158.0 暂不升级**：以上三项在 0.158.0 中都无变化，而且它把本地会话默认改为分页历史，没有收益。

### 用户确认（2026-09-29）

以上三项口径已由用户确认：
- MCP-017 按原生行为验收；
- REVIEW-006 按原生行为验收；
- 接受 timeline 的 JSON Schema 勘误。

本轮"CI 通过"不含 iOS：Mobile E2E 的 iPhone GUI 流程失败，断言为 `automations:list is visible`；Android 已通过，iOS 仍暂缓。

### 当前状态

- 本地完整矩阵 full5：**0 缺口，`complete=true`**，14926 条证据，无运行时失败。
- GitHub（cef41ee）：常规 CI 与完整协议验收均通过。
- 本地常规 CI：通过。
- 其余修复：
  - Hub 关闭通知测试不再依赖响应与通知的到达顺序（GitHub 上曾超时 10 分钟）；
  - CAPABILITY-001 夹具补齐；
  - FAILURE-007 对 SIGKILL 截断请求的判定；
  - 会话轮询对原生瞬态的容忍。
- Android UI 自动化、macOS 与 Desktop GUI 仍按用户决定暂缓；生产未部署，`releaseReady=false`。

## 最新接续：本地 Linux 完整矩阵与语义缺口补齐（2026-09-28 晚）

本节优先于下方所有历史状态。用户要求：尽量在本地暴露问题，不要每次推 GitHub CI；Android UI 自动化和 macOS 暂时跳过，其他继续推进。本轮所有提交**均在本地、尚未推送**。

### 本地复现 GitHub Ubuntu 协议矩阵

- 运行器（位于 `.local`，不入库）：`.local/linux-matrix/run.sh <label> [--control-only]` 在 Docker Desktop 的 Linux 容器中原样执行 `tools/protocol-matrix.mjs`；`.local/linux-matrix/suite.sh <label> <pkg> <Test>` 执行单个真实专项，并用本次 wire 产物做 schema 校验（约 5–20 秒）。
- 镜像 `tyrs-hand-linux-matrix:local` = 原 `tyrs-hand-protocol-linux` + iproute2、docker 客户端、Go 1.26.6（官方 SHA256）、Codex 0.157.1、bubblewrap 0.9.0（源码构建，SHA256 为首次下载固定值，上游仅提供 .asc）。
- 与 CI 对齐的三处环境差异：临时目录不在 `/tmp`（Codex 拒绝在 `/tmp` 下创建 `codex-linux-sandbox` 别名）；bubblewrap 须 ≥0.9（`--argv0`）；容器内经 `PROTOCOL_DOCKER_HOST=host.docker.internal` 访问宿主发布的数据库端口（工具已支持该变量，默认不变）。
- 校准结果：5cecc57 的本地完整矩阵 106 项缺口 = CI 36431583779 的 103 项 + 3 项本地 bubblewrap 差异（已修复并单独复验）。
- 注意：Docker Desktop 文件共享在文件刚改写时偶发读到旧视图（出现 ENOENT 或截断 JSON），重跑即恢复；不要据此判断产品问题。

### 本轮修复的真实缺陷

1. **approvalPolicy=never 时授权目录内写入被拒**（适配器 3b86c50）：never 此前映射为 SDK `dontAsk`，工作区可写时连授权目录内的 Write 也在回调前被拒。现统一走 canUseTool，never 只放行已按授权目录把关的文件编辑与已套 OS 沙箱的 Bash，越界和其他工具仍拒绝。
2. **OpenAI 专属外围能力在 Claude 侧落入 -32601 或伪成功**（适配器 ce82c15）：按用户确认的原则改为 -32004 明确拒绝；外部代理配置导入不再返回空对象伪装成功。
3. **登记遗漏**：Codex 真实发出的 `windowsSandbox/setupCompleted` 未登记；`windowsSandbox/readiness`、`account/usage/read`、`account/workspaceMessages/read` 缺响应 schema 关联。

### 本轮补齐的必需语义（均为真实 SSH 链路，本地 Linux 通过）

| 用例 | 专项 | 要点 |
|---|---|---|
| PERMISSION-001 | TestRuntimeClaudePermissionChangesRealSSH | 同会话逐回合切换工作区写入/只读/需审批/网络，真实写入、越界拒绝、审批经 fileChange 条目对应真实文件、OS 网络隔离；修复前稳定复现 dontAsk 拒绝 |
| ISOLATION-002 | TestRuntimeCrossEntryIsolationRealSSH | 两入口同名会话及 ID 互投，读取/恢复/改名/归档/分叉/提交全部拒绝，原会话与名称缓存不变 |
| MCP-003 | TestRuntimeMcpNativeIsolationRealSSH | 项目原生 .mcp.json 的 stdio 服务真实执行并回模，不外溢到其他项目或 Codex |
| FAILURE-002 | TestRuntimeClaudeCrashRealSSH | 适配器与原生 CLI 分别在回合中 SIGKILL；约 3 秒自动恢复，中断回合 interrupted/failed，Codex 不受影响，不重放 |
| EVENTS-001 | TestRuntimeClaudeEventKindsRealSSH | 同回合推理/文本/命令/文件修改条目及三类增量、用量，历史逐项一致；模型 500 以失败终态结束 |
| MIGRATION-002 | TestWorkerControlGitHubDisabledRealSSH | 按用户确认验证 GitHub 停用状态保持：遗留工作项两引擎都不领取、github 角色 410、旧 Webhook 移除、数据库禁止改指 Claude |
| CAPABILITY-003（新增） | TestRuntimeCapabilitySurfaceRealSSH | 14 项 Codex 外围能力真实透传并通过 schema；Claude 对连接器列表、Windows 沙箱与外部配置探测如实应答 |

剩余必需语义仅 REVIEW-006、MCP-017（上游 Codex 缺陷，不自编译 CLI）。

### 用户本轮决定（2026-09-28）

- MIGRATION-002 的 GitHub 部分：验证停用状态保持，**不恢复** GitHub 功能（GitHub 已于 c4398af 停用）。
- Claude 侧确属 OpenAI/ChatGPT 专属的方法：适配器 -32004 明确拒绝，并以真实 SSH 拒绝证据登记为 not-applicable；Claude 能真实提供的仍按必需实现。

### 未登记方法的探测结论与剩余难点

对真实 Codex 0.157.1 逐个探测（`.local/linux-matrix/probe-codex.mjs`，结果在 `probe/codex.json`）：

- 最小参数即成功的 14 项已由 CAPABILITY-003 覆盖。
- 需要合法参数或前置状态：`account/bedrock/setup`、`account/login/cancel`、`marketplace/add|remove`、`plugin/share/save`、`review/start`（受 REVIEW-006 影响）、`thread/approveGuardianDeniedAction`、`userVerification/verify`。
- 需要 ChatGPT 账户登录：`account/rateLimits/read`、`usage/read`、`workspaceMessages/read`、`rateLimitResetCredit/consume`、`sendAddCreditsNudgeEmail`、`plugin/share/*`。API Key 模式下原生直接拒绝；要取得成功证据须模拟 ChatGPT 后端并以 ChatGPT 登录态运行。
- 本环境原理上无法成功：`feedback/upload`（上传外部 Sentry）、`userVerification/enroll|delete`（Linux 不可用）。
- **门禁模型局限**：覆盖工具只能从“请求被 -32004 拒绝”产生 not-applicable 证据。Claude 从不发出的**通知**（如 `account/login/completed`、`windowsSandbox/setupCompleted`）即使正确登记为不适用，也永远无法关闭缺口。需要为“不适用通知”设计严格的缺席证据（例如：同一次通过的执行中，触发请求被真实拒绝或如实应答，且全部通信中从未出现该通知），这属于门禁规则变更，待用户确认。
- 另发现 0.157.1 schema 中有 58 个方法未在 runtime-matrix 登记（realtime、remoteControl、fuzzyFileSearch、hook 事件等）。门禁只对真实通信中出现的未登记方法报缺口，故这些目前不可见；是否需要全量登记待评估。

## 暂停交接：当前进展与卡点（2026-09-28）

本节优先于下方历史状态，保留此前全部记录。用户要求“把现在进展和卡点写进文档（和之前的一起）然后先停在一个稳定状态”。本轮已收尾并暂停，不开展新功能或生产部署；当前修改固定为本地提交，暂不推送触发新 CI。适配器本地 **162a8d67465a95a8f7be842340848acf1cc09d37**，主库pin一致；适配器远端仍为8072dce，主库远端仍为5cecc57。恢复时先推送适配器提交，再推送主库中对应的精确 pin。

### 当前已完成

- 上轮数据库恢复修复已在主库 **5cecc5724346684208162f4bf65090417dec3c8f** 推送：真实 SQLite 写锁导致首次打开失败后，可以在下次显式操作重试；失败连接关闭，并发仍共用打开。Linux SQLite/报告门禁7/7、客户端DB36/36、完整 `make ci-local` 和Web5/5通过。
- 客户端 v12→v13 的11张表保留与真实旧32→新33 Worker升级联合验收已接入 MIGRATION-001/005。**新 CI 尚未完成确认，暂不计该联合验收通过**；Node SQLite专项也不代表Expo原生绑定或Android手工验收。
- 本轮落实用户补充的“无法映射的可选字段不能阻断主流程”：真实 Linux SDK/CLI 复现图片 `detail` 偏好导致整批 `thread/inject_items` 返回 -32602。现在合法 `auto/low/high/original` 忽略精度偏好、保留完整正文与图片；不声称Claude提供这些精度语义。非法值、角色和身份校验仍保留。
- 新 CONTEXT-009 验证四种合法偏好、原生零模型追加、重启后实际模型请求收到四张原图与正文、无效批次不部分写入、无重复追加。连同已有事务失败恢复与分叉回退专项，Linux **3/3**；正式报文 **3执行/6 wire/159报文/107证据/0错误**。模型HTTP仅回环Mock，不代表生产模型验收。已加入正式验收清单和方法登记，原runner自动执行该测试文件。
- 本轮适配器Linux全量 **363/363，0 skipped**；build、typecheck和Biome检查通过（101 warnings/97 infos，无error），协议inventory **24/24**。所有本轮本地测试进程已正常结束，临时容器自动退出。本轮没有重跑主库完整CI；上一轮完整CI结果不能冒充本轮组合验证。
- 证据位于 `.local/validation/2026-09-27-release-goal/linux-context-detail/`：`before/`真实失败、`after/`专项、`full/`全量、`build.log`、`check.log`、`inventory.log`、`source-hashes.json`。源码与测试快照哈希一致；两份保护文件保持原哈希、不改不提交。

### 最新已消费完整结果与剩余阻塞

- **事实：**9e90576完整协议CI **36428814364 failure**，runId **80d654d4-6be3-4b18-b526-b2328074ca83**，缺口 **104 = 92未登记 + 3原生schema错误 + 9必需语义**；诊断两项已进入完整结果。运行时失败仍只有MCP017和REVIEW006，PostgreSQL无死锁。证据 `ci-9e/protocol-summary-ubuntu-24.04/`、`ci-9e/summary.json`。9e常规CI **36428814502 success**；Control已通过，28执行/57 wire/2521报文/2174证据/0错误。
- 原生阻塞：MCP分页遗漏、review缺少turn/started、timeline三项schema差异。继续保留真实失败；不修改官方CLI/SDK、原始wire或放宽协议门禁。
- 9项必需语义仍是 REVIEW-006、ISOLATION-002、EVENTS-001、PERMISSION-001、MCP-003、MCP-017、FAILURE-002、MIGRATION-001、MIGRATION-002。MIGRATION-001等待新联合迁移证据，不能按专项结果提前减算。
- fileId跨端真实内容下载、可信userVerification、Android手工验收、最终内部部署和生产验证仍未完成；Claude hosted-apps事件流仍未实现。生产未部署，`releaseReady=false`，不能宣称整体完成。
- **推测边界：**数据库打开恢复缺陷已真实复现，但没有证据证明它是此前Android真机报错的唯一原因。未登记项也不能直接解释为相同数量的产品故障。

### 恢复工作入口与范围

1. 先读取本节和根交接文档，检查两个仓库的main及保护文件；不切分支、不切/建worktree、不派代理。
2. 消费5cecc57常规 **36431583926**、协议 **36431583779** 的终态及精简证据。暂停收尾时仍未确认终态，不取消、不重复启动已有CI；重点读取MIGRATION-001/005实际报告，失败则据原始证据定位。
3. 本轮本地适配器与主库组合尚未远端完整验收；恢复推送前核实旧常规CI已结束，避免cancel-in-progress取消旧验收。适配器先推、主库后推，之后继续剩余Linux门禁。
4. macOS Worker原生验收暂缓，历史失败不算通过；Android GUI自动化skip，手工验收required；默认人工交互计时器不恢复。部署前重读release-ops及release reference，线上无版本配置先备份、保留4份。

## 当前验收范围：优先完成 Linux

2026-09-28 用户明确要求“后面 mac os 的问题也先跳过。专注 linux 的问题”。因此暂缓 macOS Worker 的原生 SSH/SDK 验收及 loopback 诊断，不再将这些已知失败作为本轮 Linux 部署的阻塞条件；历史失败保留，不计作通过。协议 CI 矩阵仅调度 ubuntu-24.04，macOS loopback job 明确 skipped，恢复入口保留。

Linux 的完整协议、Control、数据库、恢复迁移及生产验证要求不变，原生时间线 schema、MCP017、REVIEW006 和其余覆盖缺口仍须处理，`releaseReady` 继续为 false。Android GUI 自动化仍 skip，Android 手工验收仍 required。本次调整针对 macOS Worker，不改变客户端验收要求。

96ed6cd 两项 CI 已终态，常规通过、协议失败；Linux 优先策略 9467870 已推送，macOS loopback 明确 skipped。946 常规 CI 36399143808 的恢复身份测试失败，修正和验收见下文；协议 CI 36399143830 也已终态，Control通过、Ubuntu完整协议仍失败。后文关于 macOS 阻塞发布的表述为历史状态，以本段为准。

## 最新接续：客户端迁移失败恢复与完整数据保留验收

2026-09-28：Linux 真实磁盘 SQLite 复现首次打开遭遇写锁后，客户端永久缓存 rejected Promise；释放锁后再次读取仍失败。现失败后关闭未发布连接并清除失败缓存，下一次显式操作可以重试；并发读取仍共用同一次打开，没有后台循环重试或人工交互计时器。证据 `linux-mobile-migration/before.log` 与 `after-lock.log`。这证明存在可复现的恢复缺陷，不证明此前 Android 真机报错的唯一原因。

固定升级前提交 **81f6c951d83c0b0e10a97436d9605cba3b3f77ba** 的真实 v12 建表语句（完整索引和约束）作为夹具；运行真实 Worker 迁移时还会与实际提取的旧源码逐字节核对。产品 `database.ts` 通过 Node SQLite 驱动执行，不复制迁移实现。Linux 仅回环容器 **7/7**：真实锁冲突恢复、事务后段 SQL 失败完整回滚、11张表旧数据保留与新增双引擎身份约束、重开持久、未来版本拒绝且无降级/删除，以及迁移报告门禁。客户端数据库回归 **36/36**、typecheck、inventory **24/24** 通过；完整 `make ci-local` 退出0、Web浏览器5/5。最终夹具改动另有 Linux 定向回归，不将 Node 驱动测试等同于 Expo 原生绑定或 Android 手工 GUI。

MIGRATION-001 已接入实际旧32→新33 Worker/Control升级场景：旧真实 CLI 产生的历史进入客户端 v12 数据库；升级后逐字段保留草稿、偏好、缓存、未读、待核实提交和发送状态；新增 Claude 入口，再打开后使用持久化地址、端口、用户、Host Key 和密钥引用，经真实 SSH 读取对应引擎历史，并要求模型调用数不增加。只有主迁移进程、schema、资源清理及上述客户端证据同时通过，才登记 MIGRATION-001 成功。**扩展后的完整 Linux Worker 迁移尚待新 CI 验证，不能先计为通过或减少语义缺口。**

72eaecd 的完整 Linux 报告已消费：runId **a6b298c2-5ce4-4283-bf12-5648338a623d**，**106=94未登记+3原生schema错误+9必需语义**，MCP事件流登记和QUEUE-003缺报文已正式关闭；运行失败仍为MCP017/REVIEW006，PG无死锁。原始 `ci-72/protocol-summary-ubuntu-24.04/` 与 `summary.json`。9e90576 常规 **36428814502 success**，协议 **36428814364** 仍在运行；其Control **7d646028-fc4f-43d6-9312-775b5b9ab471** 为28执行/57wire/2521报文/2174证据/0错误、无运行失败、PG无死锁，证据 `ci-9e/control-runtime-summary/`。

适配器仍精确锁定8072dce，本轮未改，不重复计为新的362全量。生产未部署，`releaseReady=false`。macOS Worker暂缓；Android GUI自动化skip、手工required；默认人工计时器不恢复；两份保护文件不改不提交。

## 历史接续：Linux 双引擎进程诊断

2026-09-28：真实 Linux 适配器调用 `server/diagnostics` 返回 -32601，证据 `linux-diagnostics/before-mounted/`。首个 `before/` 因只读快照缺少 node_modules 挂载点而未启动容器，仅是夹具错误。现返回当前适配器真实 PID、RSS、活动回合及已初始化连接数；没有等价测量的 physical footprint 返回 null。计数使用独立 `claude_adapter` 名称，只描述适配器进程，不代表 CLI 子进程或 Worker 总资源，也不返回配置、路径或凭据。

DIAGNOSTICS-001 使用真实 SDK/CLI，在 AskUserQuestion 等待期间反复查询，确认 active_turns=1、问题不被回答、没有额外模型请求；人工回答完成后为0，完整重启后返回新 PID，历史仍只有原回合。正式报告 `linux-diagnostics/after/`：**1 执行/2 wire/40 报文/27 证据/0 错误**。模型仅回环 Mock，不作为生产模型验证。

DIAGNOSTICS-002 使用 Linux Go race、真实 Worker Hub/SSH 与两引擎，直接对照 `/proc/<pid>/cmdline` 和存活信号，验证实际 PID/RSS、反复只读不换进程，以及分别重启后自身 PID 更新、另一端 PID 和运行代不变，模型请求始终为0。正式报告 `linux-diagnostics/ssh/`：**2 执行/4 wire/32 报文/18 证据/0 错误**。两项已加入正式 runner、trace、方法登记和验收清单，不能从专项推算新的全量缺口。

适配器 **8072dce7878d6ea83e2c4ffbaa7b2ef9cd196c69** 已推送并精确锁定。Linux 全量 **362/362，0 skipped**，build/typecheck/Biome 检查通过；inventory 24/24，integration 增量 lint（含 staged 新文件）0 issues。主库完整 `make ci-local` 退出0，Web 浏览器5/5，日志 `linux-diagnostics/main-ci.log`。三份 Go 和三份适配器代码与 Linux 测试快照哈希相同，两份保护文件保持原哈希、不改不提交。

上一版 72eaecd 常规 CI **36426394618 success**，协议 **36426394439** 仍在运行；已完成的 Control runId **268a56e6-ac26-4c83-88f7-42a95d27ee6c**：28 执行/57 wire/2520 报文/2173 证据/0 错误，runtime failures 为空，PG 无死锁；原始 `ci-72/control-runtime-summary/`。最新完整 Linux 缺口仍以 f06 的110为准，不从本轮专项减算。生产未部署，`releaseReady=false`；macOS Worker 暂缓，Android GUI 自动化 skip、手工 required，默认人工交互计时器不恢复。

## 历史接续：Codex MCP 事件订阅的客户端隔离

2026-09-28：固定官方 Codex 0.157.1 的 MCP 事件流只支持 hosted apps，订阅属于单个连接。Worker Hub 复用上游连接时未隔离 `subscriptionId`；Linux 真 SSH 在第二个客户端创建同名订阅时实际收到 -32600 already exists，证据 `linux-mcp-stream/before-isolation/`。更早两次夹具失败分别是空线程没有 rollout、对 hosted-apps 认证头的假设错误，不计作产品复现。

Hub 现在按客户端登记订阅，每次创建使用全新的上游 ID，通知仅投递给所属客户端并恢复客户端 ID。停止、断开连接、会话退订和服务端 terminated 均释放资源；旧代通知和启动回调不能影响复用本地 ID 的新订阅。Desktop 必须仍订阅对应会话，不能借共享上游绕过退订。未修改官方 CLI、SDK、schema 或原始 wire。Claude 事件流能力仍未实现，不据此计为双引擎全部通过。

MCP-021 已加入正式 runner、trace 与三个接口的 Codex 登记。Linux Go race 真 SSH 验证 active 前请求不返回、参数/元数据实际送达、同名双客户端订阅、人工停止/断线后的另一端持续推送、服务端终止与 ID 复用、会话退订清理和零模型请求。正式报告 `linux-mcp-stream/after/runtime-wire.json`：**1 执行/3 wire/60 报文/42 证据/0 错误**。hosted-apps 服务和账户凭据均为隔离回环夹具，不代表公网账户或生产认证验证。Hub 全量 race 通过，新增迟到回调与退订单测通过，integration 增量 lint 0 issues，inventory 24/24。

4a3ab94 的完整 Linux 报告已消费：runId **bdeeb43c-dc8e-4e1a-b68c-6b7b0c0ba886**，**110=97 未登记+1 缺成功报文+3 原生 schema 错误+9 必需语义**，MCP017/REVIEW006 仍失败，PG 无死锁。原始 `ci-4a3/` 与 `summary.json`。单项报文缺口为 Claude QUEUE-003 登记的 queue/list 未在重启专项实际调用；现补齐双引擎重启后读取原条目 ID/重排顺序、无模型副作用、执行后为空的真实断言，保留原登记。Linux Control race **2 执行/6 wire/133 报文/109 证据/0 错误**，PG 无死锁，证据 `linux-mcp-stream/queue-read/`。本轮专项不能直接减算新的完整缺口。

主库完整 `make ci-local` 退出 0，Web 浏览器 5/5，日志 `linux-mcp-stream/main-ci.log`。本轮未改适配器，仍精确锁定 **e7f51376fb1845ebac42a1b31a8531bc34edc0b8**，不重复计算适配器全量。八份变更 Go 源与 Linux 编译快照的 SHA256 一致；两个保护文件保持原哈希、未修改或提交。

上一版 f06dca8 常规 CI **36423530052 success**，协议 CI **36423530055 failure** 已终态。最新完整 Linux runId **e1d8f75e-adaf-47d4-8b9d-8b583f587a79** 仍为 **110=97+1+3+9**，运行时失败仍仅 MCP017/REVIEW006，PG 无死锁；它尚不包含本轮修复。Control **d43e27cf-d349-4aaa-91b3-c9919e67e76d**：28 执行/57 wire/2525 报文/2179 证据/0 错误，runtime failures 为空，PG 无死锁。原始 `ci-f06/` 与聚合 `summary.json`。

生产尚未部署，`releaseReady=false`。macOS Worker 暂缓；Android GUI 自动化 skip、手工验收 required；默认人工交互计时器不恢复。

## 历史接续：自动审查器偏好不阻断 Claude 模型切换

2026-09-28：继续落实用户要求“不支持字段不得阻碍主流程”。真实 Linux SDK/CLI 复现 `turn/settings/update` 附带合法 `auto_review` 或 `guardian_subagent` 时返回 -32602，导致有效模型切换也被拒绝。现在这两个值保留现有用户审批，模型与 effort 正常发布；不声称 Claude 已实现自动审查器。非法审批角色、非法模型/effort 和协议外参数仍整体拒绝，官方 schema、CLI 与 SDK 未改动。

新增 TURNSETTINGS-004：实际模型 HTTP 使用更新后的模型和 effort，真实 Write 必须等待人工回答，允许后才生成文件，拒绝后文件不存在。连同既有设置隔离/重启和审批中断回归，Linux 专项 **7/7**；正式 wire **7 执行/10 wire/311 报文/239 证据/0 错误**。模型 HTTP 为隔离回环 Mock，不能当作生产模型验收。证据根 `.local/validation/2026-09-27-release-goal/linux-settings-reviewer/`，`before-renamed/` 保留两个旧实现拒绝，`after/` 保存修后真实通信。

适配器 **e7f51376fb1845ebac42a1b31a8531bc34edc0b8** 已推送并精确锁定。Linux 全量 **361/361，0 skipped**；build/typecheck/Biome 检查通过，inventory **24/24**。主库完整 `make ci-local` 退出 0，日志 `linux-settings-reviewer/main-ci.log`。两份保护文件的 SHA256 保持不变，未修改或提交。

上一版 4a3ab94 常规 CI **36421499317 success**，远端 Control 也已通过：runId **04db3a97-a376-4e39-aeca-f30424e0e461**，**28 执行/57 wire/2514 报文/2171 证据/0 错误**，runtime failures 为空，PostgreSQL 无死锁。完整 Ubuntu 协议仍在运行，不能据此推算新的完整缺口。生产未部署，`releaseReady=false`；macOS Worker 暂缓、Android GUI 自动化 skip、手工验收 required。

## 历史接续：Claude 原生队列接入 Worker 与 Control

适配器已推送并精确锁定 **f87083c6ea01937617049d34cceab5c28c896269**。

2026-09-28：Claude 实现持久化 `thread/queue/add/list/update/delete/reorder/start` 与 changed 通知。队列领取、Turn 和消息去重账本使用同一 SQLite 事务；未加载线程只持久化，重启后的只读查询不执行。已加载线程在成功回合后继续消费，失败或中断暂停；队列优先于持续目标自动续跑，结果未确认的工具不自动重放。

Worker 使用实际引擎保存队列 Journal，并验证运行时、Journal、Task 的引擎身份一致。两引擎保留独立数据根，共享执行并发槽；每条队列消息使用实际 userMessage.clientId 关联独立 Control Run、工具和审批。真实验收发现 Claude 审批取消此前只拒绝工具、仍完成回合；现已接入回合中断，原生 CLI 停止后才释放执行权。

Linux race 的六项 Claude Control 队列专项及双引擎共享并发通过：生命周期与平台工具、整 Worker 重启按重排顺序消费、审批接受/取消/显式 start、入队与已观察 Turn 两种 Journal 故障窗口、Workspace 热切换与完整重启后旧身份隔离。正式报告 `linux-claude-queue/control-complete/runtime-wire.json`：**8 执行/19 wire/588 报文/445 证据/0 错误**，PostgreSQL 无死锁。适配器队列与计划审批回归 **10/10**，正式报告 **10 执行/13 wire/450 报文/355 证据/0 错误**。模型 HTTP 仅为回环 Mock，SDK、CLI、Worker、SSH、Control 和数据库均为真实组件。

Codex 六项队列 Linux race 回归通过，正式报告 `codex-regression/runtime-wire.json` 为 **6 执行/14 wire/404 报文/347 证据/0 错误**，PG 无死锁。完整主库 `make ci-local` 退出 0，Web 浏览器 5/5；integration 增量 lint 0 issues，inventory 24/24。源码与 Linux 测试快照逐文件哈希一致，两份保护文件未修改、未提交。

最终 Linux 适配器全量 **359/359，0 skipped**，证据 `linux-claude-queue/full-verified/`；build、typecheck 和 Biome 检查通过。前次全量快照漏挂 README 导致文件读取用例失败，原结果保留 `adapter-full-final/`；补齐后该用例及最终全量均通过，没有改动产品代码来绕过失败。

Docker Linux 空网络命名空间中的关闭隧道模板不再被误判为可出网接口；检查仍要求非回环接口无地址且未启用，并取得 OS 的明确出网拒绝。官方 CLI/SDK、原始 wire 与协议 schema 未修改。Android GUI 自动化 skip、手工 required；macOS Worker 暂缓，生产未部署，`releaseReady=false`。

上一版 c639e1f 常规 CI **36416975079 success**，协议 CI **36416975103 failure**。完整 Linux runId **4453e320-9837-4a92-ada5-afa5f24c05eb**：**116=104 未登记+3 原生 schema 错误+9 必需语义**，MCP017/REVIEW006 仍失败，PG 无死锁。Control runId **a4d0cdfc-d5c5-4888-877d-d22fb1197c7e**：22 执行/43 wire/2168 报文/1882 证据/0 错误。此完整基线尚不含本轮队列，不用专项结果直接减算缺口。

## 历史接续：Codex 活动回合设置的真实生效验收

2026-09-28：新增 TURNSETTINGS-003，经 Linux 真实 Worker Hub/SSH 与官方 Codex 0.157.1 验证 `turn/settings/update`。前置条件是官方 `features.step_model_switching=true`，仅写入隔离夹具配置，未更改生产配置。默认关闭时原生返回明确拒绝；目标模型只有回退元数据或会改变已确定的 Node REPL 审查要求时也会拒绝，不能据此声称任意模型均可热切换。

测试从真实 `model/list` 确认目录，模型从 GPT-6 Astra 切到 GPT-6 Sol，下一次 HTTP 请求实际携带新模型、high effort、detailed summary 与 priority tier；再次发布 model=null 保留模型、更新 effort/summary、serviceTier=null 清除 tier。请求已发出时的旧设置不变，其他会话、未来回合及完整运行时重启后的持久默认不受活动覆盖影响。通过官方实际声明的 `functions.exec` 调用嵌套 `exec_command`，两次文件写入与真实 `custom_tool_call_output` 均有断言，恢复不重放。模型服务是隔离回环 Mock，不能当作生产模型验收。

最终专项 `linux-codex-turn-settings/persistent-defaults/`：**1 执行/4 wire/230 报文/169 证据/0 错误**，Linux Go race 通过。Codex 以外的初始化 wire 保留，但不计本次业务。不兼容模型的负例在发送前声明精确 -32600，正式门禁仍检查原生响应；既有 -32602 负例接口继续保留，不修改官方 CLI、SDK、schema 或成功门禁。inventory 24/24、integration 增量 lint 0 issues。首次探测与夹具修正的失败证据保留，包括旧工具声明假设、将线程临时 summary 配置误当作持久默认的断言；最终默认值来自真实 config.toml。

完整主库 `make ci-local` 退出 0，含 Go/race/数据库集成、移动 SSH、客户端、Android export、构建及 Web 浏览器 5/5，日志为 `linux-codex-turn-settings/main-ci.log`。共享录制器的既有 FEATURE-002 原生回归通过，正确范围报告 `trace-regression/scoped-runtime-wire.json` 为 1 执行/2 wire/63 报文/32 证据/0 错误；初次汇总误纳入仅初始化的 Codex，范围错误报告保留，未把它当业务覆盖。本轮适配器无源码变化，不重复计为新的适配器全量。

上一版 16a88d2 常规 CI **36414691615 success**，Control 远端 runId **b62934ef-2d29-4a77-a478-b2c334c75332**：**22 执行/43 wire/2168 报文/1882 证据/0 错误**，PG 无死锁、runtime failures 空；Claude 回退已进入远端 Control。完整协议 CI **36414691638** 仍在上传证据，最新已消费完整基线仍是 abac 的 119，不从本轮专项直接减算。生产未部署，`releaseReady=false`。

## 历史接续：Claude 定点回退与 Linux Control 锁序修复

2026-09-28：适配器 **19160973584f8f09105a17a68d309016d3cbae41** 已推送，主库精确锁定。新增 `thread/revert` / `thread/reverted`，按目标回合删除其自身和后续历史，保留真实原生上下文、稳定游标和提交去重记录；支持现有两种历史模式，活动回合拒绝，不撤销工作区文件。CONTEXT-008 与 SESSION-007 已加入正式验收登记。

真实 Control 验收发现并修复两个问题：

- 连续回退时，官方 SDK fork 会重建原生消息 UUID，旧代码保留旧边界，导致第二次回退找不到消息。现在通过 SDK 只读导出真实分叉快照，使用 `forkedFrom` 映射更新边界；边界、会话指针和历史删除在同一 SQLite 事务提交。映射缺失或事务失败保留原线程，不猜测消息身份，不修改 SDK 或原生 transcript。
- Control 回退先锁 Control，再由 INSERT 外键等待 Discord conversation；metadata 采用相反锁序，两次真实运行均记录 PostgreSQL 死锁。回退现复用 Session → conversation → Control 锁序。旧代码四个锁竞争场景全部失败，修后 Linux race 连续 **10 轮 / 40 子场景通过**，PG 无死锁。

Linux 适配器最终全量 **358/358、0 skipped**，build/check 通过（100 warnings/97 infos，无 error）。原生 SDK 专项 5/5，其中三个真实通信场景的正式报告为 **3 执行/8 wire/345 报文/257 证据/0 错误**；两个数据库单测不冒充 wire 覆盖。双引擎真实 Control/Worker/SSH race 专项验证旧/新回退入口、整 Worker 重启后消费原 reservation、投影复用、双端通知及实际模型上下文：**2 执行/6 wire/187 报文/157 证据/0 错误**，PG 无死锁。模型与 Discord 网络均为隔离回环 Mock，不代表生产 E2E。

证据根 `.local/validation/2026-09-27-release-goal/linux-revert/`：`full-final/`、`remapped/`、`control-final/`、`locks-final/` 为最终证据；`control-remapped/` 和 `control-race/` 的业务断言虽通过，PG 诊断失败，原始死锁证据保留。锁序测试夹具的修正失败也保留；最终修前对照在 `locks-before-v4/`。inventory 24/24，本轮 integration 增量 lint 0 issues；全仓 integration lint 另有 15 项未改文件的既有问题，不能宣称全仓 integration lint 清零。

上一版 abac706 的常规 CI **36411176515 success**、协议 CI **36411176569 failure**。完整 Linux runId **f1f8259f-14c5-40b5-8225-a354f858d963**：**119=107 未登记+3 原生 schema 错误+9 必需语义**；MCP017/REVIEW006 仍失败，PG 无死锁。Control runId **3a498f48-ae58-4f1e-8edc-4a0d7d1bb94b**：21 执行/39 wire/2063 报文/1794 证据/0 错误，runtime failures 空、PG 无死锁。报告在 `ci-abac/` 和 `protocol-abac-summary.json`；本轮回退专项不能直接用于减算完整缺口。

主库完整 `make ci-local` 退出 0，含 Go/race/数据库集成、移动 SSH、客户端、Android export、构建及 Web 浏览器 5/5；日志为 `main-ci-verified.log`。新组合远端矩阵尚待执行；生产未部署，`releaseReady=false`。macOS Worker 暂缓、Android GUI 自动化 skip 但手工验收 required、无默认人工交互计时器的要求保持。

## 历史接续：Linux Claude 历史时间线

2026-09-28：旧适配器真实调用 `thread/timeline/list` 返回 `-32601`，原始失败保存在 `linux-timeline/before/`。新增普通回合时间线读取，从 SQLite 历史返回完整条目和起止边界；最新页优先、页内升序，游标按条目身份向更早历史推进，追加回合不改变旧页。活动回合不提前生成完成边界，不触发模型。实时语音不在本次范围。

锁定版官方 TS/原生 wire 对回合边界使用驼峰字段，官方 JSON Schema 却要求下划线字段。Claude 适配器在边界同时输出同值字段以兼容两类消费者；不修改官方 schema、Codex CLI 或原生证据，Codex 的三项 schema 失败仍保留。

HISTORY-007 已加入正式 runner 和矩阵。Linux 真实 SDK/CLI 专项验证活动边界、分页拼接、追加期间游标、跨线程和跨方法游标拒绝、重启后无模型读取；1 执行/2 wire/94 报文/52 证据/0 错误。真实 Worker Hub/SSH 专项验证两线程隔离、回合边界及重启持久化；1 执行/2 wire/82 报文/51 证据/0 错误。Codex 在 SSH 专项只初始化，其原始通信保留但不计时间线验收。模型 HTTP 使用隔离回环 Mock。

上一版 de48be0 常规 CI **36408083898 success**、协议 CI **36408084272 failure**，完整 Linux runId **cb9057e6-fb97-42a6-88f4-73b08b71c86b**：**120=108 未登记+3 schema 错误+9 必需语义**。审批仲裁已进入正式覆盖；运行时失败仍为 MCP017/REVIEW006，PG 无死锁。Control runId **f401fd4f-ad2d-42dd-bd0d-ec59d5dcf690**：21 执行/39 wire/2065 报文/1796 证据/0 错误。精简证据位于 `ci-de48/`，聚合为 `protocol-de48-summary.json`；本轮时间线不能用于推算新的全量缺口。

适配器 **4d87ad28b243825551ccf3bc68ee04eeda2d3ab4** 已推送，主库精确锁定。最终 Linux 全量 **356/356、0 skipped**，build/check 通过（既有 99 warnings/97 infos，无 error）；主库完整 `make ci-local` 退出 0，Web 浏览器 5/5、integration lint 0 issues、inventory 24/24。

证据根 `.local/validation/2026-09-27-release-goal/linux-timeline/`。首次全量在未带 init 的容器中有三个进程退出断言失败；直接对照确认进程树用例留下 `Z` 状态子进程，加入 `--init` 后该用例通过且无残留，最终全量通过，诊断在 `process-diagnostics/`。保留原失败，不修改产品退出行为和测试断言。新组合远端 CI 待完成；生产未部署，`releaseReady=false`。

## 最新接续：真实多客户端审批仲裁

2026-09-28：新增 `TestRuntimeApprovalArbitrationRealSSH`，把 APPROVAL-001 从内存级检查补齐为 Linux 真实 Worker Hub、双 SSH 客户端和 Claude SDK/CLI 验收。两个客户端收到同一审批ID；客户端取消订阅后先送达的拒绝无效，仍由有效订阅者允许并执行真实Write。下一回合由另一客户端首先拒绝，首端待办收到真实resolved后再投递迟到允许，文件仍不存在，模型请求总数精确为4。

专项通过并已加入正式矩阵，实际报文经未修改的官方schema和请求闭合校验：1执行/3wire/158报文/123证据/0错误。Codex在此场景仅初始化，原始通信保留但不计为Codex审批成功覆盖。Linux Go1.26.6 race连续10轮通过；完整主库 `make ci-local` 退出0，Web浏览器5/5、lint0 issues、inventory24/24。证据根 `.local/validation/2026-09-27-release-goal/linux-approval-arbitration/`。未改产品审批逻辑、官方SDK/CLI或放宽门禁。

旧fb版完整协议CI **36404288032 failure** 已终态。最新完整Linux runId **d3d2dbb2-0574-4ee7-8b31-ff8992b6bf09**，**121=108未登记+3schema错误+10必需语义**；活动回合设置已进入正式覆盖，运行时仍仅MCP017/REVIEW006失败，PG无死锁。原始compact位于 `ci-fb/protocol-summary-ubuntu-24.04/`，聚合 `protocol-fb-summary.json`。不能从本轮审批专项直接减算新的全量缺口，仍需新完整矩阵证据；生产未部署。

## 历史接续：fileId 图片不可读时继续主流程

2026-09-28：适配器 **d453f1a2dd766cadaf2511f998299f8a0cca11b5** 修正 fileId 图片被静默丢弃的问题。正文及后续回合继续执行，同时明确告知模型图片不可读取；历史保留原始 fileId，重启不丢失。纯文本提取使用同一提示，标识不会被当作本地路径或 URL 读取。Worker 对 Discord 图片同步给出明确失败原因，并继续处理其他有效图片；客户端显示不可读及重新附加提示。

这是不可读图片的降级修正，**没有实现 fileId 下载**。附件元数据 RPC 不提供内容下载，当前锁定官方协议中也没有独立图片下载 RPC；不得据此宣称跨端图片功能完成或放宽发布门禁。

- Linux 真实 SDK/CLI 专项 2/2：实际模型 HTTP 收到提示，正文与下一回合完成，完整适配器重启后原始附件保留；模型 HTTP 仅回环 Mock。
- Linux 适配器完整 **355/355、0 skipped**；build/check 通过，既有 99 warnings/97 infos、无 error。Linux Worker 图片专项 3/3，客户端投影 8/8。
- 完整主库 `make ci-local` 已退出0，含Go/race/数据库与协议、客户端、移动SSH、Android export、构建和Web浏览器5/5；协议inventory24/24。证据根 `.local/validation/2026-09-27-release-goal/linux-fileid-fallback/`。生产未部署，releaseReady=false。Android 手工验收要求不变。
- 上一提交 fb3a41b 的常规 CI **36404288048 success**，Control 远端验收也成功：**e6f567f3-aec5-4ad8-8ae6-eac7989a6bfa**，21执行/39wire/2062报文/1793证据/0错误，PG无死锁。完整 Ubuntu 协议 CI 36404288032 仍运行，不能用本轮降级专项推算剩余协议缺口。

## 历史接续：活动回合设置与主流程兼容

2026-09-28：适配器 **e2958c55c295754b2a95e8bc1a633264bba94778** 已提交推送。`turn/settings/update` 通过真实SDK的`applyFlagSettings`修改当前回合的模型和effort，启动阶段等待原生CLI就绪；错误目标及已结束回合返回`targetUnavailable`，不写用户文件或线程默认设置。

用户明确补充“不支持字段可以拒绝，但不能阻碍主流程，必要时宁可忽略或兼容”。因此附带的合法 `summary`、`serviceTier` 在Claude没有同等语义时忽略，不阻断模型切换；不声称这些偏好实际生效。非法模型/effort、未知字段和非用户审批角色仍拒绝，完整校验后才向SDK发布。

- TURNSETTINGS-001/002验证真实模型HTTP请求的模型及effort变化、附带偏好兼容、非法组合无部分更新、其他线程和未来回合不变、重启后默认值保持、启动后立即发布。模型服务为回环Mock，SDK/CLI及持久化真实。
- 最终Linux适配器全量 **353/353、0 skipped**，build/typecheck/check通过（99 warnings/97 infos，无error）。正式wire runId **2f005cc8-8ce5-49cd-958d-d61b6f18820d**：**2执行/3wire/116报文/85证据/0错误**；inventory24/24，正式runner和Claude登记已补齐。
- 上一版主库71ff9a9常规CI **36401553185 success**，恢复身份夹具修正已获远端通过。协议CI **36401552853 failure** 已终态，Control **419d8e91-8fd2-4791-8559-32d42f831693** 为21执行/39wire/2079报文/1808证据/0错误，PG无死锁。
- 最新完整Linux基线 runId **045405c8-588b-43d0-aced-f646f3289b36**：**122=109未登记+3schema错误+10必需语义**。项目九项缺口已在正式CI关闭；运行时仍MCP017/REVIEW006失败，时间线schema三项仍存在，PG无死锁。原始报告 `ci-71/`，聚合 `protocol-71-summary.json`；本轮活动设置专项尚未计入该完整基线。

主库完整 `make ci-local` 已退出0，含生成、静态检查、Go/race/数据库与协议、移动SSH、客户端、Android export、构建及Web浏览器5/5。证据 `.local/validation/2026-09-27-release-goal/linux-turn-settings/`：修前RPC未实现、原生别名断言诊断、最终专项、源码哈希、适配器完整回归和`main-ci-local.log`均保留。原生opus别名在锁定CLI下实际解析为`claude-opus-5-5`，初稿预期4.8的失败属于测试断言错误。新版本远端矩阵仍待完成，不从专项推算新的完整缺口数。生产未部署，releaseReady=false。

## 最新接续：Linux Claude 项目持久化

2026-09-28：适配器 **2740342ba2fc1c858250ff6c6b1095534d66a338** 已提交推送，主库精确锁定该版本。生产未部署，完整门禁尚未通过。

- 新增七个项目 RPC，SQLite 保存项目及线程归属；幂等创建/导入、完整事务回滚、位置与最近活动时间分页、归属筛选、fork 继承、重启持久化及删除保留历史和文件均有真实协议验收。临时线程归属不跨进程保留，归档线程不计入项目最近活动时间。
- PROJECT-002/003 在 Linux arm64 容器、官方 SDK/CLI 和回环 Mock 模型下通过。最终专项 runId **82f55587-fbfe-4d84-b777-4aecec651512**：**2 执行/5 wire/286 报文/171 证据/0 错误**；正式 runner 与九个方法/通知的 Claude 登记已补齐。
- 最终适配器完整 Linux 回归 **351/351、0 skipped**，build/typecheck/check 通过（99 warnings/97 infos，无 error）。首次全量的 README 读取失败源于容器快照漏文件，已补齐；长等待125秒墙钟断言失败原因未确认，新增诊断后专项和最终全量均通过，原失败证据保留。未改变产品计时器、125秒阈值或官方 CLI/SDK。
- 最新已核实的完整 Linux 基线来自 9467870，runId **b35432ec-9b93-4f33-aae5-65d63e1c6f17**：**131 = 118 未登记 + 3 schema 错误 + 10 必需语义**。附件四项缺口已正式关闭，PG 无死锁；运行时仍 MCP017、REVIEW006 失败。Control runId **80405ef1-fc20-445e-9563-e2d539980741**：21 执行/39 wire/2059 报文/1790 证据/0 错误。项目专项不用于推算新的全量缺口数。

主库完整 `make ci-local` 已退出0，含生成、静态检查、Go/数据库与协议、移动SSH、客户端构建、Android export 和 Web 浏览器5/5；inventory 契约24/24。证据：`.local/validation/2026-09-27-release-goal/linux-projects/`，最终结果在 `final-full/`、`final-wire/`、`check-final.log`、`main-ci-local.log`；最初失败与长等待诊断分别保留在 `full.log`、`wait-diagnostics/`。新版本远端完整矩阵仍待完成。

### Linux 恢复身份测试的提前取消

946 常规 CI 在 coverage 阶段失败于 `TestRecoveredRemoteTerminalReplaysObservedIdentityBeforeCompletion/unavailable--OK`：已请求 events，未请求 complete。原测试给七步真实HTTP补报及Journal持久化共同限定100ms；Linux容器中给 events 响应加入150ms可控延迟，复现完全相同的六步截断。这证明夹具会被合法响应延迟打断，但不声称已证明远端唯一原因。

成功场景现在允许正常完成；临时拒绝场景在观测到实际重试计数后取消，永久拒绝仍自然结束。原请求顺序、身份、Journal保留和重试断言均保留，并增加超过旧期限的延迟响应回归；不修改产品逻辑。修后两组测试在Linux带覆盖率编译下连续20轮通过，Linux Go1.26.6容器race连续10轮通过，最终lint为0 issues。证据在 `.local/validation/2026-09-27-release-goal/linux-recovery-deadline/`；新远端CI仍需验证。

## 最新接续：Claude 附件持久化与 60fcf16 完整结果

2026-09-28：适配器 **fe30b9bdbbf65dcfd23910269615a4bcb3982517** 已提交推送，主库更新精确锁定及四个附件方法的 Claude 验收登记。生产未变更，完整测试/部署/验证目标 active。

- 新增 ATTACHMENT-002，修前真实 SDK/CLI 回合明确返回 `thread/attachment/add` 未实现。现真实 SQLite 唯一键 `(threadId, attachmentType, identityKey)` 支持幂等创建，重复添加保留原 payload，包含 null/false/0/空字符串等 JSON 值；仅真实创建和删除通知，稳定分页绑定线程，删除会话在事务内清理附件。
- 用例验证两个真实模型回合、并发重复添加、类型及线程隔离、limit=1 完整分页、删除分页边界后继续、跨线程游标及无效参数拒绝、三次重启持久、历史及源文件不变。模型仅回环 Mock，不宣称图片上传或 fileId 下载。
- 适配器完整 **349/349、0 skipped**，build/typecheck/check 通过；check 仍为 99 warnings/97 infos、无 error。仅回环沙箱专项的正式 wire/schema 通过：runId **346e29a7-4931-4b50-86b9-f4fed47bd3a2**，**1 执行/4 wire/169 报文/102 证据/0 错误**。正式协议 runner 已包含此用例，主库 inventory 契约 24/24。
- 60fcf16 常规 CI **36387466142 success**，协议 **36387466138 failure**。最新 Linux 完整 runId **8e397c41-804e-4308-bcff-ddb2d1457a27** 的基线为 **135 = 122 未登记 + 3 schema 错误 + 10 必需语义**，PG 无死锁。原生附件登记已计入，时间线三个 schema 错误在 Linux 同样复现；运行时仍 MCP017/REVIEW006 失败。本轮 Claude 附件尚未计入该旧基线，不直接减去四项冒充新全量结果。
- 该轮 Control-runtime **faffa529-19e9-4970-b592-01934aaf26fb** 通过：21 执行/39 wire/2060 报文/1791 证据/0 错误。macOS14 **f9b05cb4-e024-4a78-b1d5-b22c3468a92b** 五项失败，parallel-approvals 和 files 的原始 job 日志明确回环 TCP `operation not permitted`；turn-control 原始 Claude wire 明确返回 API EPERM，另两项为既知 MCP017/REVIEW006。macOS15 comparison 的原始 report 明确本轮 6000/6000、passed=true，仅证明该探针通过，不消除 macOS14 业务失败，具体拒绝规则仍未证明。
- 更新 pin 后的主库完整 **make ci-local 退出 0**（2026-09-28T08:22:22Z 核实），Go/race/数据库与协议集成、移动 SSH、客户端、Android export、构建和 Web 浏览器 **5/5** 通过。两个保护文件哈希未变，不改不提交；Android GUI 自动化 skip，手工 required，原计时器手工长等待证据保持不变。

证据根 `.local/validation/2026-09-27-release-goal/`：`claude-attachments-before.log`、`claude-attachments-full.log`、`claude-attachments-native/`、`claude-attachments-final/`、`protocol-60-summary.json`、`ci-60/`。默认人工交互计时器已移除；原生时间线 schema、MCP017、REVIEW006、macOS 网络原因、可信 userVerification、图片 fileId 和剩余完整协议缺口仍须解决，不能部署生产。

## 最新接续：附件验收、时间线 schema 差异和回环诊断

2026-09-28：基于已推送的 29d8482，适配器仍固定且干净为 f667d85；生产未变更，完整测试/部署/验证目标 active。此段优先于下文旧基线。

- 29d8482 常规 CI **36384551584 成功**，协议 CI **36384551530 失败**，均已终态。Linux 原始 runId **dbb62ef8-dc72-446b-a9de-78d67eaeed80**：最新完整缺口 **137 = 127 未登记 + 10 必需语义**；PG 无死锁，运行时仅 MCP017/REVIEW006 失败。FAILURE-008 的实际 processCount=10、processTree 长度=10，记录三个所属进程组，测试 socket 已清理，cleanupErrors 为空；MIGRATION-007 和其他恢复/迁移均通过。不得继续把 138/149 当作最新。
- 该轮 Control-runtime 的 **692e6a6b-5e73-41b9-b710-39da816ef21a** 为 21 执行/39 wire/2063 报文/1794 证据/0 错误，PG 无死锁。macOS14 **ad87cdc8-a472-48b1-bb1f-adf35840d2b9** 有九个失败专项；runtime/files/config/review/mcp-oauth 的日志直接报回环 EPERM，history 和 goal 的原始 wire 含 API EPERM，goal 另有 ECONNRESET，另两项为 MCP017/REVIEW006。
- 已补取 d42 原始 wire，原本未定因的 Claude review 与 bootstrap 均明确返回 API EPERM。macOS 系统拒绝日志与部分失败的程序、目标端口和时间匹配，但未标明具体拒绝规则，不能断言唯一根因。诊断进程退出 0 不等于连接全通过：d42 实际 **5994/6000**，含 1 次 EPERM、2 次重置、3 次超时；29 为 **5993/6000**。新 summary 显式区分 processStatus 与 passed；四项回归拒绝空、重复、非法和不完整结果，不重试业务、不放宽沙箱、不改变原验收结果。本地单次 6000/6000 不覆盖远端失败。
- 新 **ATTACHMENT-001** 真实 Codex/SSH 验收附件创建、同身份幂等且不覆盖 payload、limit=1 分页、同键不同类型及跨线程隔离、真实通知、两次重启持久、删除保留另一线程、历史和源文件。精确两次回环 Mock 模型请求，Claude 仅初始化且运行代不变。最终 race **a54bb4ce-19d5-4d60-992b-bad2d206ab35**：**1 执行/3 wire/108 报文/74 证据/0 错误**。只证明附件元数据，不等同 fileId 图片上传或下载。
- 新 **HISTORY-006** 单独验收普通回合时间线的原生反向翻页、完整页面对账、起止边界、线程隔离和重启持久。初稿误把 nextCursor 当作向后翻页，原始位置 12→8 明确其向更早历史推进；已按原生行为修正并对账完整页面。最终 race **07514b4a-3773-499a-9e32-d46296505553** 的业务断言通过，但正式 wire/schema **失败**：原生 turnStarted/turnCompleted 使用 turnId/startedAt，官方 JSON schema 要求 turn_id/started_at。1 执行/2 wire/92 报文/49 有效证据，3 类 schema 错误；不能计为协议通过，也没有撤销此用例或改 schema。
- 用同一官方 Codex 0.157.1 CLI 重新生成 experimental JSON schema，ThreadTimelineListResponse 与仓库逐字节相同，SHA-256 **b03238a894858bc70bdb59165b0038ac2723fcdc17e685e82b47e39b9a9c4f43**。因此不是靠重新生成即可修复的旧文件；未修改官方 CLI、SDK、响应或门禁，差异继续阻塞发布。
- 本轮完整 **make ci-local 退出 0**（2026-09-28T06:21:41.739Z），含 Go/race/数据库、客户端、Android export、构建和浏览器 5/5。随后只拆分新附件/时间线专项，分别重新跑 race 与正式 wire；integration lint 为 0 issues，inventory 与新增诊断契约 28/28，恢复/迁移/PG/诊断契约 21/21。两个保留文件 SHA-256 未变，不提交；Android GUI 自动化仍 skip，手工 required。

证据根 `.local/validation/2026-09-27-release-goal/`：`protocol-29-summary.json`、`ci-29/`、`ci-d42/macos-wire-detail/`、`native-attachments-separated/`、`native-timeline-separated/`、`native-timeline-schema/generated/`、`native-attachments-final/`。混合早期失败在 `native-attachments-timeline/` 和 `native-attachments-timeline-final/`，不覆盖。新增两个专项还需新提交的完整远端矩阵；不能从本地成功推算完整缺口数。

## 最新接续：SIGKILL 所属进程组的完整清理

2026-09-28：基于已推送的 d42e680，适配器仍固定 f667d85。只修改迁移/恢复测试夹具，生产未变更，releaseReady=false。

- e41 的 Linux MIGRATION-007 失败于回滚后的 old-worker-3 被 SIGKILL 后，测试 socket 仍有监听。new-worker-2 正常退出；原始报告没有记录被杀进程树，不能由现有报错断言唯一原因。本地原样 macOS 回滚通过，正式校验 2 执行/8 wire/351 报文/235 证据/0 错误，仍保留远端失败。
- 新增真实进程回归：中间父进程退出，监听子进程保留在本测试创建的独立进程组中。旧实现只取当前 PPID 树，漏杀已重归属的监听者，稳定复现相同“上代测试中继 socket 仍有监听进程”错误。
- 夹具只把组长在本次 Worker 父子树中的独立组纳入退出范围，排除继承自主测试进程的共享组；同时记录真实 PID/PGID 审计。继续等待实际进程退出并探测 socket 已无监听后才清理旁路入口，不强删活 socket、不按进程名查杀、不改官方 CLI 或产品逻辑。
- 回归修前失败、修后通过；新增断言确认另一同名进程的独立组保持运行。四项进程清理回归均通过，迁移/恢复/PG 契约合计 17/17。待审批恢复改为使用实际终止时的统一进程范围，避免更早的第二份快照覆盖真实记录。
- 最终真实三种迁移与三种恢复组合全通过，runId e7a07a3d-382e-456b-8811-9f1e8cc581fc：**10 执行、26 wire、1121 报文、780 证据、0 错误**。证据 native-crash-group-final/scoped-result.json；模型为回环 Mock，Worker、数据库、SSH、CLI/SDK 均真实。该组缺陷已实证修复，但仍需 Linux 新 CI 验证原失败，不以本地通过关闭远端问题。
- d42e680 常规 CI 36383010337 已成功。协议 CI 36383010328 的 Control-runtime 成功：21 执行/39 wire/2066 报文/1797 证据，无 PG 死锁。macOS14 的 PROJECT-001 正式执行通过；该任务另有七项失败，除既知 MCP017/REVIEW006，runtime、files、codex-events 直接报回环 TCP EPERM，Claude review 为 failed，bootstrap 未到达 Mock 模型。后两项尚不能归因于网络；没有 skip 或重试覆盖原失败。
- 最新完整覆盖仍取自 e41 原始 Linux runId b3e2fc78-4f28-4081-a03d-42646e0d8d74：**149 = 136 未登记 + 2 缺成功 wire/schema + 11 必需语义**。APPROVAL-002 和 FAILURE-006 已在该轮成功，但 MIGRATION-007 新失败增加相关缺口；不继续使用旧 148/147 基线，也不从本地专项推算 d42 全量数字。

证据根 .local/validation/2026-09-27-release-goal/：native-rollback-group-before.log、native-rollback-group-after.log、native-rollback-baseline/、native-crash-group-final/、ci-e41/migration-007-detail/、ci-d42/macos-job.log。d42 的完整本地 CI 已通过；本轮夹具改动另由上述完整迁移/恢复组合及 17 项契约验证。

## 最新接续：原生项目管理与已完成工具恢复边界

2026-09-28：基于主库 `e41a9ff`，适配器固定 `f667d85`，新增 PROJECT-001 并修正恢复验收的前置条件。生产未变更，`releaseReady=false`。

- PROJECT-001 已完成真实 Codex/SSH 的项目增删改查、同幂等键创建与导入、分页排序、线程导入及 metadata 迁移、重启持久化、删除解除归属且保留历史。目录哨兵文件不变，精确一次回环 Mock 模型调用，Claude 运行代不变且只初始化。
- 实测重启后仅 `thread/read` 未收到删除项目的线程归属通知；加入真实 `thread/resume` 后，已加载线程收到 `thread/project/updated`。保留最初失败，不将只读历史视为已加载会话。
- 最终普通与 race 均通过，分别为 runId `35fdb4bc-e0e8-4317-88f9-b6a2740cbe4f`、`e9501031-87a3-446c-877d-cbf405e3fda7`；每轮正式 wire/schema 为 **1 执行、3 wire、116 报文、74 证据、0 错误**。只计 Codex 业务，Claude 初始化原始 trace 保留。已登记九个项目方法/通知及正式矩阵入口，inventory 契约 24/24。
- 21452da 的常规 CI `36379782185` 成功，协议 CI `36379782159` 已失败；最新完整 Linux 基线是 **148 = 136 未登记 + 12 必需语义**，runId `43785c18-4483-479b-9e7b-f6222fb4004e`。新增 FAILURE-006 的原始报告已提取；不再使用 0f 的 147 作为最新基线。
- FAILURE-006 的两个业务恢复断言均通过，但 Claude 旧进程在 SIGKILL 前发出的 `thread/name/set` 没有响应。静态的“当前无待办请求”快照后，后台标题同步仍可能发起请求；重启后的标题重试成功没有消除旧请求，因此原 schema 门禁正确报红。原始请求为 connection `51512:2`、id 19，时间 `2026-09-28T05:12:42.949Z`。
- 恢复夹具先等待真实 Control 标题任务完成和同线程、同连接的原生标题成功响应，再进入已完成工具待补报的 SIGKILL 场景。新 helper 拒绝跨连接响应、旧成功掩盖新待办、重启成功掩盖旧待办、错误与重复响应；回放原 CI wire 仍拒绝原失败。没有改产品、CLI、schema 或恢复门禁，没有清空 pending，也没有新增固定等待。
- 最终三种恢复场景 FAILURE-006/007/008 全部通过，runId `bd3a35f0-c431-4c3b-a071-716ec8563994`；正式 wire/schema **4 执行、10 wire、369 报文、251 证据、0 错误**。007/008 的 Codex 初始化原始记录保留，不冒充其业务执行。门禁及前置条件契约 16/16，并加入正式 CI。
- 本轮项目源码的完整 `make ci-local` 退出 0，结束于 `2026-09-28T05:38:05Z`，包括 Go/race/数据库/移动 SSH、客户端、Android export、构建和浏览器 5/5。项目 integration lint 0 issues；恢复前置条件另由最终真实三场景与 16 项契约验证。
- e41a9ff 的常规 `36381360839` 已成功；协议 `36381360841` 中 Control-runtime、macOS-loopback 成功，macOS14 仍仅 MCP017/REVIEW006 失败，Linux 待终态。Control 原始 runId `84a9b98f-9d90-40c8-979f-cb6dd128ec01`：21 执行、39 wire、2064 报文、1795 证据，无 schema 错误或 PG 死锁。本地通过不直接用于减去完整覆盖缺口。

证据根 `.local/validation/2026-09-27-release-goal/`：`native-projects-resumed/`、`native-projects-race/`、`native-projects-final/`、`native-recovery-title-final/`、`ci-214/failure-006-detail/`、`ci-e41/`。`native-projects-membership/` 保留最初失败；Android GUI 自动化继续 skip，手工验收要求不变。

## 最新接续：原生审批取消与停止通知

2026-09-28：新增 APPROVAL-002 的真实适配器/SDK/CLI 验收，仅模型 HTTP 为回环 Mock，未修改官方 CLI 或协议门禁。生产未变更，`releaseReady=false`。

适配器 `f667d85db4464c05649b462eb3963e58798d318b` 已提交并推送，主库 adapter-lock 精确更新；完整适配器回归 **348/348，0 skipped**，build/typecheck/check 通过，check 为 99 warnings、97 infos、无 error。

- 同时保持两个会话审批：中断一个后真实回送迟到允许，旧文件保持不存在、模型不续写、条目只结束一次；另一会话仍可批准并产生精确文件内容。
- stdin EOF 和 SIGTERM 两种关闭方式均验证旧审批失效、原地恢复保留旧回合且不重放；只有显式新回合的新审批可产生新文件，模型调用数精确为三次。
- 首次三项业务断言通过，但正式 wire/schema 拒绝 SIGTERM 缺少 `serverRequest/resolved` 的记录。根因是 `stop()` 先设置 stopped，使随后审批取消的结束通知被统一抑制。现先同步结束待审批，再抑制退出过程的其他通知；没有将断线当作成功答案或放宽校验。
- 修复后增加停止前精确一次 resolved 断言，三个专项全部通过；正式 wire/schema 为 **3 执行、5 wire、127 报文、100 证据、0 错误**，runId `6113dbef-c2c9-484c-957b-73dd148432b6`。测试已加入正式适配器协议矩阵，远端全量仍待该新版本验证。
- 真实 SSH 的 APPROVAL-005 重连、中断与运行代重启回归在 race 下通过。仅 Claude 业务通信按正式 schema 验证：1 执行、5 wire、197 报文、140 证据、0 错误；附带的 Codex 初始化 trace 保留但不冒充 Codex 审批验收。证据 `native-approval-lifecycle-ssh/claude-runtime-wire.json`，runId `e5e976aa-4f18-45de-8f16-141c8b4f944f`。
- 更新锁定版本后的完整 `make ci-local` 已退出 0：生成、静态检查、Go 单测/race/数据库集成、移动 SSH、客户端、构建、Android JS export 和浏览器 5/5 通过。日志与退出状态为 `native-approval-lifecycle-final/ci-local.log`、`ci-local-status.json`。Android GUI 自动化保持 skip，未运行 Maestro。

证据 `.local/validation/2026-09-27-release-goal/native-approval-lifecycle/` 保留修前失败，`native-approval-lifecycle-final/` 保留修后结果。此本地专项不用于直接重算下文 0f75642 的 147 项远端覆盖缺口。

## 最新接续：跨 Workspace 队列与正式 Control 组合通过

2026-09-28：主库基线 `0f75642`、适配器 `cb02aa2`。本段优先于后文历史进度；生产未变更，`releaseReady=false`。

- 新增 QUEUE-007，覆盖绑定热切换、变更前完整 Worker 重启，以及执行完成后再次完整重启。绑定变更由数据库夹具注入，真实 Control/PostgreSQL/Redis/Worker/SSH/官方 CLI 决定执行结果；模型与 Discord 网络为本地 Mock，不宣称验证了管理后台重绑入口。
- 旧队列保留入队时的 Run、Task、Workspace 身份，原生自动回合的平台工具真实返回“Workspace 绑定已失效”，两个 Workspace 均无旧任务副作用；原 Run Journal 保存真实终态。新 Workspace 新会话仍能创建唯一任务，并落到其独立 completed Control Run，避免以禁用全部工具掩盖串权。
- 新绑定不认领旧线程 metadata；执行完成后再次重启，旧队列为空、历史保持两回合、模型总调用五次，工具和模型均未重放。QUEUE-007 已加入正式 Control 与完整协议矩阵。
- 普通专项通过；补齐最终 Journal 和再次重启断言后的 race 专项通过。正式 wire/schema 为 **1 执行、7 wire、162 报文、137 证据、0 错误**，queue/add 和 queue/list 均有成功响应；integration lint 0 issues，inventory 契约 24/24。
- 最终源码完整 Control 组合退出 0，runId `3406b854-4079-4ce8-9179-b9357522c66a`：**20 执行、37 wire、2113 报文、1859 证据、0 错误**；PostgreSQL 无死锁，runtime failures 为空。它是 macOS Control 子集，不等同完整协议矩阵。0f75642 的完整 `make ci-local` 已通过，但在新增 QUEUE-007 之前执行。
- 已推送的 0f75642 常规 CI `36377959228` 成功；协议 CI `36377959242` 失败，Control-runtime/macOS-loopback 成功，Linux/macOS14 仍仅在 MCP017 分页和 REVIEW006 审查运行时专项失败。最新 Linux 原始 runId `c83dadc3-453e-48e8-a9fe-ff68098cf4c3` 的完整覆盖为 **147 缺口 = 136 未登记 + 11 必需语义**；TITLE-001 与 APPROVAL-009 的两项成功 wire/schema 缺口已关闭。该远端结果不包含本轮新增 QUEUE-007。

证据根 `.local/validation/2026-09-27-release-goal/`：`native-queue-binding-race/`、`native-queue-binding-control/`、`protocol-0f-summary.json`。可信 userVerification、fileId 图片跨端、其余全量协议缺口及生产发布仍待完成。

## 最新接续：补齐标题与终端审批的原生通信证据

2026-09-28：在主库 `959a863` 和适配器 `cb02aa2` 基础上，只为 TITLE-001、APPROVAL-009 的真实集成测试增加 CLI 入口旁路录制。生产未变更，`releaseReady=false`。

- 标题辅助客户端的 `config/read`、Control 仲裁后提交给 CLI 的终端审批答案不经过 SSH 客户端；此前仅记录 SSH 报文，因而缺少这两项成功响应证据。
- 测试复用现有字节透传录制器，按真实连接分组保存原生报文，不改变请求、响应、通知或全局 CLI 环境变量。检查录制错误和原生进程退出；未交付下游的迟到响应不得计作成功。
- 固定工具链、仅回环网络沙箱、真实 Control/PostgreSQL/Redis/Worker/SSH/官方 CLI，模型仍为 Mock。两项正常与 race 专项通过；合并正式 wire/schema 门禁得到 **2 个执行、4 个 wire、410 条报文、370 项证据、0 错误**。
- TITLE-001 具有真实 `config/read` 成功响应；APPROVAL-009 的取消与允许答案均在原生上游记录成功，SSH 的两个 resolved 仍按 interrupted 计数，未放宽门禁。
- integration lint 为 0 issues，协议覆盖与 wire 门禁契约 17/17 通过。证据位于 `.local/validation/2026-09-27-release-goal/native-upstream-final/`；最初缺少固定 CLI 环境变量的失败保留于 `native-upstream-title/`。
- 原 959a863 的远端协议验收 `36376750911`：Control-runtime、macOS loopback 已成功；macOS14 仍仅报 MCP017 分页和 REVIEW006 审查失败，Linux 全量尚在运行。本地补证据结果不能代替该版本的新远端全量覆盖。

## 最新接续：移除默认人工交互计时器

2026-09-28：适配器 `cb02aa202914182f733144527e3779177f93e3ea` 已提交并推送，主库精确更新 adapter-lock。此段替代下文关于“人工等待问题未修复”的当前状态；历史失败证据保留。生产未变更，`releaseReady=false`。

- 移除 PendingInteractions 的默认 120 秒计时器，回答、主动取消、连接断开、回合中断和运行时停止继续准确一次结束请求。
- 移除 NativeMcpBridge 的默认 120 秒工具时限。固定 MCP SDK 不支持关闭 request 计时器，因此默认工具请求改经公开 transport 等待，保留 SDK 的工具输出校验、进度、取消通知与断线清理。未修改官方 Codex 或 Claude CLI/SDK，也没有用超大 timeout 冒充关闭计时器。
- 握手、管理请求、用户显式设置的 `tool_timeout_sec` 和第三方服务自身的执行上限仍有效。移动手工夹具去除显式 `tool_timeout_sec:120`；夹具服务自身设十分钟清理预算，仅用于测试，不是产品的人工回答时限。
- APPROVAL-007、EVENTS-006 保留并更新语义为真实等待 125 秒后仍可回答、拒绝或中断；补齐 MCP form/url 长等待及中断后迟到接受无副作用。没有删除或 skip 这些协议用例。
- 最终适配器全量 345/345 通过、0 skipped，build/typecheck/check 通过；check 有 99 warnings、97 infos、无 error。另有仅回环网络隔离的原生专项 20/20、管理/OAuth及等待回归 21/21，主库移动夹具与协议清单契约 44/44 通过。未将旧 d19 的完整主库 CI 当作新版本结果。
- Android API35 AVD 手工从新会话发起 MCP FORM DECLINE，原始 wire 记录等待 **176589ms** 后回答有效。界面显示 `_OK`，Run `4c92bd39-68ac-4f2b-896c-66d80d23f89e` 为 completed，恰好一个 resolved 和回合终态，无副作用文件。自动化 GUI 继续 skipped，未运行 Maestro。
- 手工复验复用原 Control/数据/配对与 APK，重新编译适配器并重启隔离 Worker；Worker 二进制未重建。启动时夹具遗留 Unix socket 导致暂时连接失败，确认 ECONNREFUSED 后清理该 socket 并恢复。模型仍为回环 Mock，未覆盖外部真实模型、实体 Android 或外部 URL 页面。

本轮证据位于 `.local/validation/2026-09-27-release-goal/timer-removal-native/`，包括全量与专项日志、原生长等待 wire、`manual-result.json` 和起始/超过120秒/完成截图。原 12 场景汇总保留为旧适配器的历史结果。完整协议覆盖、可信 userVerification、fileId 跨端、跨 Workspace 队列及生产部署仍未完成；149 项缺口仍仅是 d19 的已核实基线，未对新版本全量重算。

## 最新接续：Android 手工正向场景与冷启动完成

本段优先于后文历史状态。当前 main 产品基线为 `d19b07a32badae49766eb547f4ca9fbaf9507fc5`，适配器仍固定 `25bf4ee`。Android 自动化 GUI 保持 skipped，手工 GUI 已完成下列范围；生产未变更。

- 在 API35 AVD 上手工完成 12 个正向场景：Codex CHAT，Claude CHAT/FULL/APPROVAL/DENY/PLAN，MCP FORM 和 URL 的 ACCEPT/DECLINE/CANCEL。逐项核对界面、12 个不同 Control Run 的 completed 终态及实际文件副作用。
- 表单接受覆盖 `count=0` 的无效提交与修正为 3 后继续、note 输入后清空、`ratio=0` 和 `enabled=false` 类型保留。最终文件恰好一行，完整 JSON 与预期等值，不仅检查 `_OK` 回复。拒绝和取消不生成副作用文件。
- 通过 Android 系统设置强制停止 Dev 客户端后重新打开；双引擎机器及 Control Worker 关联、项目、各自历史仍保留。Codex 列表只见自己的会话，Claude 恢复对应表单接受历史。
- **已发现但未修复**：适配器 `pending-interactions.mts` 与 `native-mcp-bridge.mts` 各有默认两分钟计时。PLAN、FORM DECLINE 和本轮 FORM ACCEPT 出现人工等待期间失败；FORM ACCEPT 后续 1 分 4 秒提交成功。失败截图保留，快速重试通过不消除长等待问题；具体先触发哪一层尚未从报文确认。本轮未修改适配器或超时策略。
- 范围限制：真实 Control/PG/Redis/Worker/SSH/官方 CLI/SDK，模型为回环 Mock；没有执行实体设备或真实外部模型验收。URL 场景未打开外部确认网页。后端沿用此前启动进程，未在 d19 提交后重建；尚未结束环境并生成模型夹具退出汇总。
- d19 常规 CI `36369543209` 与 Android-only `36369780776` 成功；协议 CI `36369543204` 失败。新 Linux 原始 runId `1438ff64-eca0-4508-82df-ab5a3d9ba27f` 确认仍有 **149 项缺口（136 未登记、2 无成功 wire/schema、11 必需语义）**，PG 死锁检查通过。两个已登记证据缺口为 TITLE-001/config/read 与 APPROVAL-009/item/commandExecution/requestApproval；运行时失败仍为 codex-mcp-pagination、codex-review。

手工汇总：`.local/validation/2026-09-27-release-goal/android-manual-v3/manual-acceptance-summary.json`；最新协议摘要：同证据根下 `protocol-d19-summary.json`。APK SHA-256 仍为 `90062d630932d42fb44a98d54b7e4a343fe0120730fbb2869d596c7343da7b9a`。本轮未修改产品代码，既有完整本地 CI 结果仍对应 d19 产品基线。

人工等待生命周期、完整协议门禁、可信 userVerification、图片 fileId 跨端和真实跨 Workspace 队列端到端证据仍待处理。正向手工场景完成不等于整体验收或生产发布条件满足。

## 最新接续：队列已推送与 Android 相机手工修复

本段优先于后文。当前 main/HEAD 为 `94bc990`，已推送；适配器仍为 `25bf4ee`。Android 自动化 GUI 保持 skip，手工 GUI required；生产未变更。

- 常规 CI `36365638232` 成功；协议 CI `36365638252` 失败，Control-runtime/macOS loopback 成功，Linux/macOS14 运行时均只报 `codex-mcp-pagination` 和 `codex-review`。
- 最新 Linux 原始完整覆盖 runId `521591da-5693-4982-9818-05bc331a11d3` 为 **149 项缺口（136 未登记、2 无成功协议/schema 证据、11 必需语义）**，PostgreSQL 死锁检查通过。此基线替代后文 152；仍不满足发布门禁。
- Android 手工扫码在无摄像头设备上复现 CameraX 未捕获初始化异常。`expo-camera@17.0.10` 精确 patch 将错误交给 onMountError，并保留协程取消语义；Android autolinking 明确从源码构建该模块，避免预编译 AAR 忽略补丁。源码 APK 同路径手工复验不再崩溃并显示相机不可用提示。
- 关联页允许粘贴链接，权限拒绝或无摄像头时仍可关联。手工发现 URL 解析异常会回显完整输入，现为 URL/schema 提供固定错误提示，新增 3 个脱敏回归。客户端 371 passed / 2 既存 skipped，typecheck 通过，lint 0 errors / 6 既存 warnings。
- 双引擎 SSH/关联与项目已手工配置。最终 APK 手工验证无摄像头回退及无效链接固定错误提示通过；Claude CHAT 显示预期回答，Control 唯一对应 Run 为 completed。其余 11 个聊天、审批、计划及 MCP 场景尚未完成，不宣称 Android 全部验收通过。
- 最终源码完整本地 CI 已退出 0：Go/race/数据库/SSH、生成与构建、客户端 371 passed / 2 既存 skipped、Android JS export、浏览器 5/5。源码哈希已逐文件复核；最终原生 APK 构建安装成功，SHA-256 为 `90062d630932d42fb44a98d54b7e4a343fe0120730fbb2869d596c7343da7b9a`。证据为 `android-camera-final-ci.log`、`android-camera-final-ci-status.json`、`android-camera-final-build.log` 与 `android-manual-v3/`。

原有两个保留文件不纳入提交；可信 userVerification、图片 fileId 跨端、真实跨 Workspace 队列端到端、其余协议缺口与官方两项失败继续阻塞最终发布。

## 最新接续：Control 组合通过与完整 CI 夹具修复

2026-09-28 接续核验：当前 main/HEAD 仍为 b6422ae，生产未变更，releaseReady=false。Android 仅自动化 GUI skip；手工 GUI 必须完成，当前仍 pending。

- 修复后的正式 Control 组合 runId 567541df-f277-47c1-b2e3-e45b02834a41 已通过：19 条引擎执行、28 份 wire、1702 条报文、1506 条证据，schema 校验无错误，PostgreSQL 死锁门禁通过、runtime failures 为空。它不代表完整协议矩阵通过。
- 首次完整 make ci-local 在 HTTPAPI 临时数据库启动阶段失败：TestWorkspaceProjectScanSynchronizesMissingAndRecovery 报 port "5432/tcp" not found，尚未进入业务断言。HTTPAPI 夹具现复用 CI 显式提供的 PostgreSQL 服务，并为每例创建、清理独立数据库，不清空其他用例数据。
- 确定性夹具回归修前失败、修后通过；原失败用例和事件 6 场景、审批登记/恢复 12 场景均在 race 下通过。最终 integration-tag 增量 lint 为 0 issues；完整 make ci-local 复跑退出 0，生成、lint、Go 单测/race/数据库集成、手机 SSH、构建、Android JS export 与浏览器 E2E 5/5 通过，核心覆盖率 80.8%。
- Android --dev-real APK 已构建安装；旧手工环境退出后，现重新启动专用模拟器和隔离 Control/Worker/双 SSH 环境。全程未启动 Maestro；本轮尚无手工场景通过证据，不能用安装成功代替验收。

证据：`.local/validation/2026-09-27-release-goal/queue-lifecycle-control-matrix-v2/`、`queue-lifecycle-final-ci-local.log`（失败）、`httpapi-ci-database-baseline/tests.log`、`httpapi-ci-database-fixed/tests.log`、`queue-lifecycle-lint-final-v2.log`。完整 CI 复跑日志为 `queue-lifecycle-final-v2-ci-local.log`；恢复后的手工环境证据独立存入 `android-manual-v3/`。

## 最新接续：原生队列生命周期与审批锁序

本段优先于后文历史结果。当前基线为 b6422ae；以下新增实现尚在工作区验证，不能复用旧提交的完整 CI 作为通过证据。

- 原生队列现在在入队前保存 Workspace 身份、Task/Run、输入和 cwd；按实际 userMessage.clientId/turnId 建立独立回合，复用正常回合的 Control、Journal、事件、工具与审批链路。保留原生自动消费，不另发 turn/start。官方队列 schema 没有 additionalContext，参与者信息只进入本地授权快照。
- 新增 QUEUE-002～006 正式验收：update/delete 与连续回合、reorder 后完整 Worker 重启、接受/取消审批与 queue/start 成功、admission 和实际回合已落盘但 Run Journal 写入失败两种恢复窗口。真实历史只读对账、原 Run ID 和模型仅执行一次已有通过证据；未知/仍 active 的状态不会擅自重放，真实跨 Workspace 变更端到端仍未覆盖。
- 修复非 userMessage 的 item 事件被丢弃；真实生命周期断言 userMessage/dynamicToolCall/agentMessage 全部进入 agent_events。修复后台心跳读取已复用 Gin Context 的 race，后台改用进入 handler 时捕获的 request context。
- 修复 displaced Run 审计 SQL 参数类型，以及事件、prepare、steer 的数据库锁序；12 个数据库场景已通过。新增正式 Control 组合 runId 5eadcf7a-8b94-4ce4-bcda-0e087eb86bb8 的 19 条引擎执行、28 份 wire、1712 条报文、1516 条证据通过 schema 校验，但 PostgreSQL 记录两次审批恢复/事件上报死锁，因此组合整体失败。
- PG 原始日志明确记录恢复审批持有 Run 等待 Control、事件上报反向等待。新增确定性两引擎回归共 12 场景，修前 10 失败/2 通过；统一审批登记、恢复与事件的 Session → Control → Intent → Run 父记录锁序后，12 场景及 6 个事件锁序场景、原生审批与双端仲裁在 race 下全部通过。没有添加数据库盲重试或放宽门禁。
- 固定 Go 1.26.6、GOTOOLCHAIN=local 的 integration-tag 增量 lint 为 0 issues。修复后的完整 Control 组合正在复跑（queue-lifecycle-control-matrix-v2）；本轮完整 make ci-local、最终提交远端验证尚未完成。
- b6422ae 常规 CI 36331187494 和 Android-only 36331187898 成功，协议 CI 36331187489 失败。Linux 原始完整覆盖 runId cee4d476-252c-48d5-8027-d30850944129 为 **152 项缺口**：138 未登记、3 无成功协议/schema 证据、11 必需语义未通过；Linux/macOS14 的运行时专项失败仍为 MCP017/REVIEW006。此完整基线替代后文 156 项，不能由 control-only 子集推算完整缺口数。
- Android 自动化 GUI 保持 skip，手工 GUI 仍 required/pending。本轮已启动专用模拟器并构建 --dev-real 包，尚无手工通过证据。可信 userVerification、fileId 跨端图片、完整协议覆盖和官方两个失败专项继续阻塞发布。生产未变更。

证据根：`.local/validation/2026-09-27-release-goal/`。本轮关键证据：`queue-lifecycle-control-matrix/`、`queue-interactive-lock-baseline.log`、`queue-interactive-lock-fixed.log`、`queue-lifecycle-lint-pinned.log`；先前 queue-lifecycle-complete 的 Gin race 失败保留，不记为通过。

## 最新接续：队列共享并发与 Android 验收范围

本段优先于后文历史结果。2026-09-27 用户明确要求跳过 Android **自动化 GUI** 用例，**手工 GUI 验收仍是必需项**。

- 已取消正在运行 GUI 阶段的 Mobile E2E 36328003987，实际结论为 cancelled，不改写成 passed 或 skipped。后续自动化入口和 CI 按 acceptance-policy.json 标记 ssh-setup/suite 两阶段 skipped，并输出 JUnit 与范围报告；手工状态为 pending、mobileAcceptanceComplete=false。构建、协议、SSH 与非 GUI 回归继续执行，iOS 仍按本轮安排暂缓。
- 自动化 skip 策略、安装隔离、证据拒绝规则、构建与既有契约共 23 项通过。损坏的策略会拒绝继续安装；skip 证据不能通过完整移动成功门禁。未执行本轮 Android 手工验收。
- 2a33914 的远端常规 CI 36327995287 已成功；协议 CI 36327995308 已失败：Control-runtime/macOS loopback 成功，Linux/macOS14 运行时失败仍为 MCP017、REVIEW006。新 Linux 原始完整覆盖 runId 37207d87-60c6-49e9-80dd-57bf7d74e77d 为 **156 项缺口**：143 未登记用例、2 无成功协议/schema 证据、11 必需语义未通过；PG 死锁门禁通过。这一基线不包含本轮尚未提交的队列改动。
- 已真实复现并修复原生队列绕过共享并发：限额为 1，Claude 被模型屏障阻塞时，旧 Worker 仍允许 Codex queue/add 实际调用模型。修复在原生输入前预留共享槽，以 clientUserMessageId 和实际 turnId 跟踪自动回合；直接回合和同线程队列使用引用计数共用槽，连续队列结束或明确删除后释放。
- QUEUE-001 真实双 SSH/官方 CLI/Claude SDK 回归通过：双向满额拒绝、两条连续自动回合、删除待执行项、删除已取出项返回 false、完成后额度释放，以及 CLI 重启后 52 条原生持久队列的两页恢复、resume/start 满额拒绝和无重放。官方队列最多 100 项；初稿 102 项被官方正确拒绝，已改为每页 50 项、共 52 项，未改 CLI 限制。
- 未知入队响应保留状态不重发；快速回合先于入队响应、并发删除先于入队响应、初始化时已出队项、重复终态均有状态回归。队列事件流丢失不能当作回合结束，旧运行代退出后才释放其持有资源。
- 最终定向回归使用 race、临时 HOME、虚拟凭据及仅回环出站沙箱，runId 812a9675-1122-409d-bf0d-fb804727e657（queue-final-v2/，包含重启边界的客户端引用快照保护）。正式 runtime-wire 门禁：4 引擎执行、9 wire、282 报文、184 证据，0 错误；增量 integration lint 0 issues，inventory 契约 24/24。最终源码完整 make ci-local 退出 0：生成、lint、Go 单测/race、真实数据库集成、手机 SSH、构建、Android JS export 与浏览器 E2E 5/5 通过；核心覆盖率 80.7%，客户端 368 通过/2 个既存跳过。日志 queue-gui-skip-final-ci-local.log；不等同完整协议或 Android 手工验收通过。
- 队列专项不是完整队列功能验收：Control intent/run、逐回合 Journal、工具/审批精确归属、绑定身份快照、完整 Worker 重启恢复，以及 queue/start 成功执行仍待补齐。可信 userVerification、图片 fileId 跨端、其他覆盖缺口和官方 MCP017/REVIEW006 继续阻塞发布；Android 手工验收也尚未完成。生产未变更，releaseReady=false。

证据：`.local/validation/2026-09-27-release-goal/queue-capacity-model-baseline.log`、`queue-final-v2/`、`queue-integration-lint-final-v2.log`、`android-gui-skip-contracts.log`、`protocol-2a33914-linux-summary/`。

## 已完成

- CLI、CI、工具链、手机端、协议清单与适配器切换到精确版本 0.157.1。
- Worker 和手机 SSH 版本探测只接受 0.157.1，拒绝更高版本、预发布版和自定义构建后缀。
- 通过官方 CLI 重新生成 `protocol/codex-app-server/0.157.1/`，保留 0.147.0 作为升级差异基线。
- 新增请求实际为 35 个，删除 `thread/rollback` 1 个，净增 34 个；新增通知 13 个。
- 新请求已在 Hub 分类，新请求和通知已进入 runtime-matrix；未实际执行的能力仍保留 required 和空用例，不计作覆盖通过。
- 老 Desktop 的 `thread/rollback` 已登记为显式扩展契约，响应仍校验新版 Thread；Codex 运行时转换已完成，见 Phase 2。
- 适配器 Thread 输出必填 `projectId: null`，CLI shim、initialize 与 runtime/info 版本一致。
- 客户端测试与预览补齐新版字段，处理 functionCallOutput、新协作工具枚举和 completed 子任务活动。
- fileId 图片保留附件标识，不再假定一定有 URL；尚未实现通过 fileId 下载图片。
- 手机 MCP 表单区分 openaiForm 与用户身份验证；身份验证卡片不提供伪造成功的确认按钮，完整链路仍待实现。

适配器提交：`25bf4ee06b2daee45bc3d36e81837f6d248f0877`。

## 完整测试推进（2026-09-27，优先于下文历史验证）

- 最新基线为 d5e8940：常规 CI 36324743015 成功；协议 CI 36324743007 失败，Linux/macOS 14 的运行时专项均仅 MCP017、REVIEW006 失败，Control-runtime/macOS loopback 成功。Linux 完整覆盖原始结果为 157 项缺口，runId b8b14fc7-d80f-41a8-94a0-a83a30dbfaf1，替代以下 6640499 的 169 项历史记录。
- 本地 Android 完整 GUI 在 d5e8940 APK 上通过（SSH 设置 4m22s、主流程 8m36s）：12 个模型场景、schema/语义和资源回收均通过。运行包含保留的 shared YAML 工作区修改，不能替代干净提交 CI；远端 Android 36324762397 已失败，详情见下。本轮未复现 database is locked，不能宣称历史问题已修复。证据：.local/e2e/evidence/2026-09-27T141153682Z-mobile-android-dual-engine/。
- Control 组合 1009a65b 再现两条 Session 外键死锁。标题/metadata 的 Session 锁现改为 FOR NO KEY UPDATE：最初 8 个真实数据库回归旧实现全失败、修复全通过，写互斥、旧锁顺序、人工标题、租约和引擎隔离仍通过。最终源码组合及完整 make ci-local 已通过；后文 0502d2d9 组合成功属于历史结果。
- control-only 增加正式 runtime-wire.json 门禁，复用官方 schema 与完整清单，仅豁免未执行能力的全量覆盖要求，24 项契约通过。新门禁正确拒绝上述失败组合。configWarning 按 d5e8940 原始证据中的 MIGRATION-006/007 登记 Codex；Claude 仍保持缺口。清单变更后的全量覆盖尚未重算。
- 再次审计发现 1df32c8c 组合虽然业务/wire 通过，PG 仍有 metadata 与终态 Session 消息序号的反向等待。回合 fence 改为从真实 Run 关联先锁 Session，再锁 Control/intent/run；同时修复入队的 Session 强锁。10 个外键插入场景、2 个两引擎完成场景均有修前失败/修后通过证据，连同既存锁序共 19 场景通过。新增 PG 日志门禁能拒绝这类隐藏死锁。
- 最终冻结源码组合 c32066a9-4a5c-4e1c-bef7-1532adade0d4 已退出 0：13 专项、14 引擎执行、21 份 wire、1468 条报文与 1304 条协议证据；runtime-wire.json 和 postgres-diagnostics.json 均通过，runtime-failures.json 为空。它涵盖最后的入队锁修复，替代 e601593c；不等同完整协议矩阵通过。integration-tag 增量 lint 为 0 issues。证据位于 .local/validation/2026-09-27-release-goal/session-locks-control-final-v2/。
- 完整 CI 的 v2 复验退出 2：覆盖率阶段的 Discord 测试在数据库初始化时报 port "5432/tcp" not found，业务断言尚未执行；同时只读检查确认该临时容器已启动却没有宿主端口映射。现让 Discord 测试复用 CI 显式提供的 PostgreSQL 服务，每个用例创建和清理独立数据库；未改变生产代码、未添加重试。新增回归旧实现失败、修复后与原失败用例一同通过，验证同一服务、数据隔离、不清空前例与退出清理。integration-tag 增量 lint 为 0 issues。证据：session-locks-final-v2-ci-local.log、discord-ci-database-baseline.log、discord-ci-database-fixed.log。
- 最终 v3 完整 make ci-local 已退出 0：生成、lint、Go 单测/race、真实数据库集成、手机 SSH、构建、Android JS export、浏览器 E2E 5/5 均通过；核心覆盖率 80.8%，客户端 368 通过/2 个既存跳过。日志：.local/validation/2026-09-27-release-goal/session-locks-final-v3-ci-local.log。这不是完整协议矩阵或新 Android GUI 的通过结果。
- 远端 Android 36324762397 已失败：SSH 设置通过，主流程 4m35s 在退出计划确认时找不到提交按钮，截图证实卡片底部超出视口。本轮没有 device offline/database is locked 证据。两次问答提交前补 scrollUntilVisible，待新提交 GUI 验证；iPhone skipped，完整移动验收未通过。
- 历史 CI 基线：6640499，常规 CI 36321807435 成功。协议 CI 36321807423 的 Control-runtime 和 macOS loopback 成功；macOS 14 仍有 MCP017（分页遗漏）和 REVIEW006（缺少 turn/started），Linux 另有 Codex 表单死锁与 Claude 权限专项跨用例 Discord 请求。后两项已有直接证据和修复，d5e8940 的两平台 CI 未再出现；本轮新锁修复仍需新提交 CI。
- 历史完整覆盖基线为 6640499 的 Linux CI：169 项缺口（runId cf6ea44e-8cbe-4667-868c-80332bf03c50）。当前原始完整基线为本节首条 d5e8940 的 157 项；本地 control-only 子集不用于重算整个项目的缺口数。
- 标题隔离、数据库隔离、两处锁顺序修复与 Android 诊断改动后的完整 make ci-local 退出 0：Go 单测/race/真实数据库集成、手机 SSH、核心覆盖率 80.7%、构建、Android JS export、浏览器 E2E 5/5。客户端 368 通过、2 个既存跳过，6 个既存 ESLint warning、0 error。最后补充的终端通知清单通过 20 项协议契约及真实组合 wire 重验。日志：.local/validation/2026-09-27-release-goal/title-locks-ci-local.log。仍不代表完整协议或 Android GUI 通过。
- 前一提交 8e70412：常规 CI 36317234697 全绿；协议 CI 36317234788 的 Linux 与 macOS 14 均只剩 MCP017（分页遗漏）和 REVIEW006（缺少 turn/started）失败。Control-runtime、macOS loopback 成功，恢复/迁移六项不再失败；不能因此宣称完整协议覆盖通过。
- 原生队列的独立失败验收已复现：空闲线程 queue/add 自动完成真实模型回合，但 Control 没有对应 intent/run。queue/start 的响应和 turn/started 没有输入，实际 userMessage 的 clientId/content 才能用于关联。队列仍未实现，不能只补一个方法分支或关闭原生自动消费来绕过。
- 队列设计还必须涵盖入队/恢复前的共享并发预留、连续自动回合的独立 Journal、工具/审批的精确回合绑定、绑定身份快照与重启恢复。以上是待实现约束，不是通过证据。
- 本次扩展表单工作树的完整 make ci-local 退出 0：生成检查、Go lint、前端类型/lint/测试、Go 单测/race/真实数据库集成、手机 SSH、80.7% 核心覆盖率、构建、Android JS export 和浏览器 E2E 5/5 通过。客户端 368 通过、2 个原有真实连接用例按原配置跳过，ESLint 保留 6 个既存 warning、0 error。日志为 .local/validation/2026-09-27-release-goal/forms-ci-local.log；不代表 Android 真机、队列或完整协议覆盖通过。

- 历史提交 4c3e2c2 的完整 `make ci-local` 曾通过，核心覆盖率 80.6%，浏览器 E2E 5/5；当前表单工作树的验证结果以上一条为准。
- 首次完整运行只在浏览器服务启动时撞到已有本地服务的 18080 端口；未停止该服务。脚本改为申请空闲回环端口、检查本轮服务进程，第二次完整执行退出码为 0。
- Codex 审批与并行审批切换到 CLI 实际声明的 exec_command，校验真实命令输出、审批回答和文件副作用；两引擎并行专项通过。
- Codex 事件使用真实 PTY 命令，客户端收到 item/started 后释放文件屏障，再验证 outputDelta、终态聚合、补丁、历史和实际文件；四场景全部通过。短命令终态输出存在但无流分片已保留为失败证据，未伪造事件。
- MCP 八场景通过：普通 MCP 内容按 input_text 数组解析，含 structuredContent 的 stdio-write 按真实字符串解析，保留管理、表单/URL 接受/拒绝/取消、副作用与重载隔离断言。
- 组合真实 SSH 验收七专项通过：events、MCP、approvals、parallel-approvals、permission-grants、files、Claude event-gaps。全部使用临时 HOME、虚拟凭据与回环 Mock LLM。
- 324501c 的常规 CI 36313951393 失败于审批重连测试等待超时；代码检查发现等待 resume 响应时会丢弃提前到达的审批。改为同时接收两者且设置读超时，修后 race 重复 200 次及全 Hub race 通过。原用例本地 100 次未复现，不能将 CI 根因推测写成直接复现。
- 同提交协议 CI 36313951469 已结束：Control 与 macOS 15 loopback 通过，Linux/macOS 14 协议矩阵失败。macOS 14 的文件上传、权限和 Claude readonly 日志记录回环连接 EPERM；对应本地组合通过，但尚不能认定 CI 环境问题已消除。
- `releaseReady=false`，生产未变。完整矩阵、新能力、上游 MCP017/REVIEW006、迁移/故障与 Android 门禁仍需完成。

证据：`.local/validation/2026-09-27-release-goal/`（完整 CI、失败 CI、重连回归）及 `.local/validation/2026-09-27-codex-phase2/acceptance-fixtures-final/`（七专项组合）。

## Phase 2：原生回退与 Control 替换

### Codex 标题任务的宿主工具隔离（2026-09-27）

- 真实缺陷：官方 CLI 忽略 default_tools_enabled。旧代码经真实 Control 标题任务执行原生 JS 注册表探针，仍有 16 个宿主工具，包括命令、补丁、图片、目标、协作及继承的 MCP。
- 标题会话改用官方配置开关与 environments=[]；先读取该辅助目录的有效配置，按字面名称逐项禁用已有 MCP。配置读取失败时不启动标题任务，不修改宿主配置。Claude 保留其有效的 default_tools_enabled 参数。
- 新增 TITLE-001 正式矩阵专项：真实 SSH 发起普通任务，Control 自动调度标题，官方 CLI 执行注册表探针；修复后宿主工具注册表为空、结构化标题写回成功、Outbox 清零，普通会话仍保留 MCP，配置文件逐字节不变。
- 官方 gpt-5.6-luna 仍提供无宿主工具的隔离 JS 编排入口 exec/wait；本次验证的是宿主命令、文件、MCP 等能力不可达，不宣称协议中完全没有工具入口。
- 单测、真实专项及包含数据库隔离和两处锁顺序修复的 Control 组合已通过：13 个专项、14 条引擎执行；21 份真实 wire、1443 条报文通过正式清单与官方 schema 校验。该组合 runId 为 0502d2d9-445b-4b7d-8006-afb7ea47d6cd，PostgreSQL 日志无死锁；完整本地 CI 退出 0。
- 证据：.local/validation/2026-09-27-release-goal/title-control-baseline/（旧实现真实失败）、title-control-final/（修复后真实通过）。

### Control 专项隔离与并发死锁（2026-09-27）

- 真实验收原先共用数据库，全局 Outbox 可领取前一专项残留消息；唯一 Worker 身份不足以隔离。每个 fixture 现使用同一临时 PostgreSQL 服务内的独立数据库。确定性回归验证后一 fixture 看不到前一条 sentinel，且前一数据未被清空。基础设施专项单独登记，不冒充协议能力覆盖。
- 6640499 的 Linux PostgreSQL 日志证实标题确认与标题生成存在 Control/Outbox 反向加锁。Apply 和 FailDelivery 现先处理或锁定 Control，再更新 Outbox；仍在同一事务校验租约，旧标题结果/错误不能覆盖新版本，失效租约完整回滚。旧实现四场景全部失败，修复全部通过。
- 独立数据库组合进一步复现 Live 入队与 metadata 的 Session/Control 死锁。metadata 现先锁 Session，再锁 Conversation/Control；同时修复查询错误被空 conversation 分支吞掉的问题。name/settings/lifecycle 三种真实 API 请求的锁等待回归均有旧失败和修复通过证据。
- 标题隔离后不再向模型暴露辅助 cwd；Live 模型夹具改按真实 json_schema 输出格式识别标题任务，未恢复宿主环境或减少隔离。
- 真实 stdin 用例产生的 item/commandExecution/terminalInteraction 已补入正式清单，Codex 关联 APPROVAL-009；Claude 未有对应覆盖，仍保留 required 和空用例。协议 inventory 契约 20/20 通过。
- 最新组合的 13 个专项和 14 条引擎执行全部通过；21 份 wire 正式校验无报文、schema 或未登记方法错误。它不包含队列失败草稿，不等同完整协议或 GUI 通过。
- 证据：.local/validation/2026-09-27-release-goal/ 下的 fixture-isolation-baseline/、fixture-isolation-fixed/、thread-name-lock-baseline.log、thread-name-lock-fixed.log、metadata-lock-baseline.log、metadata-lock-fixed.log、title-locks-control-final/。

### Android 构建与模拟器错峰（2026-09-27，GUI 仍失败）

- 事实：既有 CI 在启动模拟器后才执行 Gradle Release 构建，此前 Android 验收出现 device offline。资源竞争是否为掉线根因尚未证明。
- 工作流改为先准备 SDK/AVD，按已配置的 x86_64 目标构建并校验 APK，再启动模拟器；安装阶段读取真实设备 ABI，重新检查 native-code、全部 .so 目录及 ELF 头，禁止重新构建。
- 构建脚本新增仅 Android 使用的 --build-only/--install-only；默认本地构建安装流程不变。验收入口拒绝 --build-only，避免只构建后继续使用旧安装。CI 明确指定 emulator-5554，AVD 路径跨步骤保留。
- 门禁脚本与移动契约 39/39 通过，包括没有设备时可独立构建、安装不重复构建、缺失 APK、ABI 不符、查询失败及真实设备拒绝；Bash 和工作流 YAML 语法检查通过。这些替身测试不计作真实 APK 构建或 Android GUI 通过。
- 脚本改动后的完整 make ci-local 再次退出 0，包含浏览器 E2E 5/5、核心覆盖率 80.7% 和 Android JS export；这仍不是原生 APK 或模拟器验收。完整日志为 .local/validation/2026-09-27-release-goal/android-stages-ci-local.log。
- 6640499 的真实移动 SSH 预检已通过：双 SSH、计划、权限、审批、六个 MCP 场景、结构化标题、引擎隔离与 wire schema；证据 .artifacts/mobile-runtime/1790514688269/。
- Android-only Mobile E2E 36321807989 已失败：真实 APK 构建、ABI 校验、模拟器启动和安装均成功；21:35:17 CST，Maestro clearState 后的 am force-stop 返回 device offline，reverse 映射同时丢失，宿主 Control/双 SSH TCP 始终可达。错峰未解决掉线，不能再把资源竞争当作已确认根因。iPhone job 按要求 skipped。
- 本地只读模拟器用现有 9 月 26 日旧 APK 跑最小启动流程成功，boot ID/adbd PID 稳定；这是诊断，不能作为当前工作树 GUI 验收。测试模拟器已停止。
- 增加启动前系统 logcat 和连续设备身份采样（get-state、boot ID、uptime、adbd PID），诊断契约 5/5 和真实本地采样通过；这仅提高下一轮取证能力，不宣称掉线修复。database is locked 仍未复现与修复。
- Android 失败原始证据：.local/validation/2026-09-27-release-goal/android-6640499/；本地最小诊断：android-local-driver/（diagnosticOnly=true）。
- 证据：.local/validation/2026-09-27-release-goal/android-build-stages-final.log。

### 扩展 MCP 表单（2026-09-27）

- 修复两个实际缺陷：Go 归一化拒绝官方 openaiForm；Worker 初始化未声明扩展表单能力，导致官方 CLI 对扩展 MCP 方法返回 -32601。
- Worker 的 socket/stdio 和手机握手现在均声明 openai/form 与 openai/elicitation.form，使用现有 JSON Schema 校验；没有声明尚未实现的用户身份验证能力。
- 新增 MCP-020 真实验收：标准 form、旧扩展 openai/form、新扩展 openaiForm 各覆盖 accept/decline/cancel，共九场景；原生 MCP SDK 调用由官方 CLI 转换成相应 app-server 模式。
- 已通过定向真实 Control/PG/Redis/Worker/SSH/CLI 验收：早到错误类型答案不能抢占合法答案，Discord 越界答案被拒绝，数值和布尔类型保持，接受只写一次文件，拒绝及取消均不写，模型结果只回传一次，Control Run 完成且 Outbox 清零。
- 使用 Go overlay 恢复旧解析器后，前六场景成功，第七个 openaiForm/accept 在 Control 返回 400“不支持的 MCP 交互模式”；修复后的九场景全部通过。能力未声明时的原生 -32601 失败证据单独保留。
- 正式 control-only 矩阵从仓库根执行测试二进制，11 个专项、13 条引擎执行全部通过；20 份真实 wire 独立校验无 schema 错误。MCP-020 的 313 条报文及原生表单回答正确关联本轮 runId 和 caseIds。
- 该组合包含两引擎断网恢复、双引擎 Control、三端接力、原生回退、stdin 审批、Claude MCP/权限、新 Codex 表单及重启链路。它不包含队列失败草稿，也不等同完整协议、Android 或 GUI 通过。
- Go 五包 race、客户端握手与表单测试 19/19、TypeScript/改动 ESLint、integration-tag 改动 lint（0 issues）及 Node 协议/恢复/迁移契约 30/30 通过。
- 临时数据库清理使用 finally，日志目录不可写时仍回收本轮容器和 socket；真实故障注入已验证，保留写入错误而非伪称证据保存成功。

证据：.local/validation/2026-09-27-codex-phase2/control-codex-forms-capabilities/、control-codex-forms-old-parser/、control-codex-forms-native-methods/；正式组合位于 .local/validation/2026-09-27-release-goal/forms-control-matrix/runs/124a0e65-14dc-4f4b-a11e-60bd4de71dbd/，含 control-validation-summary.json。契约和资源回收日志位于同一 release-goal 目录。

### 终端输入审批与首次建帖死锁（2026-09-27）

- 手机卡片和 Control/Discord 问题按原生 kind=writeStdin 显示“终端输入审批”，保留真实命令、输入、原因、目录和原生可选决策。
- APPROVAL-009 已实现并登记真实矩阵：官方 CLI 启动 PTY，在后续回合降为只读，产生独立 stdin 审批；取消后无续写和副作用，允许后只写入一次。两个回调共享终端 itemId，但 approvalId 不同；Control 原样保存请求参数与回答，Discord 卡片含审批类型。
- 原生 writeStdin 在该场景仅提供 accept/cancel，不伪造 decline。测试断言原生 interrupted 与 Control canceled 一致，后续回合仍使用同一真实终端。
- 新验收连续两次复现 PostgreSQL 死锁。数据库日志明确显示首次建帖持有 Control 等 intent，而 ConfirmTurn 持有 intent 等 Control。提交、确认、完成和对账现在先经 fence 锁 Control，再锁 intent/run，未添加盲重试。
- 确定性真实数据库回归使用锁等待和 NOWAIT：通过 Go overlay 编译旧源码时四条路径全部失败，当前源码全部通过；迟到确认不重开终态的旧回归也通过。
- 最终真实组合通过：stdin 审批、原生回退/替换、双引擎 Control 和三端接力；数据库日志无死锁。Go 相关 race、integration-tag 改动 lint（0 issues）、客户端类型检查/改动 lint、审批测试 5/5 与协议契约测试 18/18 通过。
- 证据：.local/validation/2026-09-27-codex-phase2/control-stdin-deadlock-evidence/、submission-lock-old-baseline/、submission-lock-fixed-v2/、stdin-lock-combination/。临时 Control 数据库日志现在随矩阵保留，便于核查锁等待。

### 恢复与迁移补充（2026-09-27）

- 官方 0.157.1 的 Unix 监听入口已变为符号链接，原夹具按 lstat().isSocket() 判断导致提前退出。仅对 Codex 录制器入口支持该原生形式，仍拒绝普通文件及活监听；物理 socket 留给官方 CLI 回收。
- 真实 SIGKILL 测试进一步发现父进程退出时后代尚未释放监听。夹具现在等待刚捕获的本次 PID 树全部退出，再验证监听关闭；不按进程名查杀，也不删除活监听。
- FAILURE-006/007/008 和 MIGRATION-005/006/007 本地全过，包含真实 Control、数据库、Worker、SSH、CLI、崩溃、补报、不重放、旧32升级及回滚再升级，独立 schema 与进程收尾校验全部通过。Node 回归 10/10。
- 证据目录：.local/validation/2026-09-27-release-goal/recovery-migration-socket-v2/；前一轮失败证据保留在 recovery-migration-socket/。本地通过仍需 Linux CI 验证。
- 4c3e2c2 的协议 CI 36316392532 已结束失败；Control 和 macOS15 loopback 成功，macOS14 仅余 MCP017/REVIEW006，先前 EPERM 本次未复现，不能认定彻底消除。Linux 结果另行跟进。
- 队列实测新增明确缺陷：空闲时 queue/add 自动运行并完成模型回合，但 Control 没有对应 intent/run。userMessage 通知包含实际输入与 clientId，turn/started 和 queue/start 响应均不包含输入。不能只给 queue/start 补 switch；自动、连续、恢复回合的并发额度、工具/审批绑定、身份快照和 Journal 均需接入。当前保留失败验收，未将其计作完成。

- Codex 的数量回退先通过原生降序分页解析明确的 beforeTurnId，再调用 thread/revert；legacy 明确拒绝。Claude 继续使用自己的原生 rollback。
- 原生 thread/revert 保留空 turns 与原生分页游标；旧 rollback 响应从原生 turns 分页补齐历史。没有改写 rollout、模拟历史或重新请求模型。
- paginated 的 thread/items/list 透传原生不透明游标；legacy 保留既有读取实现。页大小 0/1001、反向游标、跨线程拒绝及回退后不泄漏已删条目均通过真实分页回归。
- Control 管理的线程只允许回退已确认的最新回合；更早锚点在执行前拒绝，防止原生历史与 Control 投影分离。
- Worker 在变更前原子持久化回退 Journal；回退后的新回合消费原 replacement reservation，Worker 重启后仍保留该关系。普通线程不新增 Control 前置请求。
- 原生响应丢失时仅只读对账；无法确认则保留待对账状态。若确认已回退，补报 Control 并返回明确的 -32053/applied 提示，不伪造成功响应或游标。
- SESSION-006 通过真实 Control、PostgreSQL、Redis、Worker、SSH、官方 CLI 验证：两次回退、整 Worker 重启、原生上下文删除、reservation 消费、投影位置复用和 Outbox 收敛。模型与 Discord 网络使用回环夹具。
- SESSION-003 另验证原生 thread/reverted 通知和重启后的会话历史；Claude session 允许原生 settingsUpdated 通知先到，同时保留目标通知断言。

事实：旧 Hub 把 thread/revert 返回的原生 items 游标按自定义 Base64 解码，真实 SSH 返回“历史游标无效”；现已通过原生游标回读保留回合。
事实：初次 Control 回退验收发现后续新回合没有消费预留记录，confirmed turn 为空；现已补齐 preflight/start，重启与投影回归通过。

## Phase 2：本地验证与 CI

- 全量 `go test ./internal/... ./mobile/sshtransport/...` 通过；Codex、Worker、Hub 的 race 通过。
- 默认 CI Go lint 与 integration-tag 的改动增量检查通过。扩大到 integration tag 的完整检查仍有 11 个既存问题，均位于本轮未修改的 live_integration_test.go、host_desktop_real_integration_test.go、app_server_failures_test.go；新增的 2 个问题已修复。不能将该扩展检查标记为全绿。
- 协议 inventory 与 mobile E2E 契约测试 23/23。
- 官方 CLI + 真实 SSH：Codex session/items、Claude session、Codex Control 回退及重启、Claude 三端接力、两引擎 Control 断网回归均通过。
- 真实数据库：两引擎 Desktop 与 Discord 绑定/替换生命周期通过。
- 常规 CI 中四个失败用例已修复并逐一真实重跑通过：扩展 responseFile 读取、两个模型默认值假设、托管模型密钥隔离用例的旧 shell 工具夹具。
- MCP019 与完整 OAuth 生命周期均在官方 0.157.1 上真实通过：资源 origin 收到指定业务头，授权 origin 不接收该头，授权、工具文件副作用、结果入模及重启凭据复用均通过。原夹具只接受字符串，而实际 MCP 工具结果已为 Responses input_text 数组；按真实报文更新解析后保留了所有隔离断言。
- 基线 5df7c35 的 CI 36310159677 与协议 CI 36310159684 均已结束为 failure；本地定向通过不代表这两个旧 CI 或全量 matrix 已通过。
- 本轮未重跑客户端 GUI、Android、完整 make ci-local 或全量双引擎 matrix；生产未变，iOS 遗留改动未纳入提交。

证据目录：`.local/validation/2026-09-27-codex-phase2/`，包含失败复现、最终 suite 日志及 status.json。

PostgreSQL 就绪探测改为 127.0.0.1 TCP。事实是一次本地数据库初始化窗口产生 EOF，修正后多次定向测试通过；“旧探针命中初始化临时 Unix socket 服务”为推测，尚未证明是旧 CI relay 失败的根因。

## Hub 偶发归档单测

事实：旧用例在 RPC 响应后才建立订阅，但创建通知异步分发，响应完成不代表通知已经完成分发。
该用例本地重复 100 次及带 race 重复 1000 次均未复现 CI 的失败。

推测：CI 中尚未分发的 `thread/started` 在订阅建立后到达，导致断言提前把它当成归档事件。
已让测试在创建前订阅，并显式校验创建通知，再校验归档和取消归档顺序。
本轮没有修改 Hub 的事件分发行为，也不声称已直接复现原失败。

## Phase 1 本地验证（历史）

- `go test ./internal/... ./mobile/sshtransport/...` 通过。
- `go test -race ./internal/appserverhub/... ./internal/worker/... ./internal/bootstrap/...` 通过。
- golangci-lint v2.12.2：Codex、Hub、Hostworker、手机 SSH transport，含 integration build tag，通过。
- 客户端 TypeScript 类型检查通过；单测 366 通过、2 个真实连接用例按原配置跳过。
- 客户端 ESLint：0 errors，6 个既存 warnings。
- 协议 inventory 与 mobile E2E 契约测试：23/23。
- 适配器 build 与全量 npm test：338/338；Biome 无错误，仍有既存 warnings/infos。
- 新增接口的真实执行证据、全量双引擎协议 matrix、真实 SSH 接力与 Android GUI **尚未在最终组合重跑**。

## 后续必须完成

1. Session 锁修复的本地完整 CI、Control、wire 与 PG 门禁通过，仍需新提交两平台 CI 及完整协议覆盖；events、approvals 等已完成的专项不能等同完整覆盖通过。
2. `thread/queue/start` 已分类 controlled，但 Controller 仍需接入真实生命周期；Claude 新接口仍需实现及执行证据，不能将分类当成已实现。
3. writeStdin、Go 侧 openaiForm 归一化与扩展能力协商均有本地及两平台真实链路通过证据。用户身份验证的可信完成、拒绝及取消链路仍未完成。
4. 新增 ThreadItem、HookMetadata、图片 fileId、异步问题等需要完整跨端行为验收，当前类型检查通过不等于能力验收。
5. MCP019 在本地和两平台 CI 真实通过；MCP017、REVIEW006 在最新 d5e8940 的两平台 CI 仍失败，不制作自编译 CLI。
6. Android 最新 CI 已越过启动和 SSH 设置，失败于计划确认提交按钮超出视口；滚动修复待新提交 GUI 复验。历史 device offline 和 database is locked 尚未证实根因与修复。iOS 继续暂缓，不能将跳过算作完整移动门禁通过。
7. 保留原交接中的语义专项和未执行方法缺口，完成后再考虑生产门禁。

官方协议参考：[Codex App Server](https://learn.chatgpt.com/docs/app-server)。
本次具体接口以固定 CLI 生成的 schema 为准。
