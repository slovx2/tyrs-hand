# Codex 0.157.1 升级进度

更新时间：2026-09-27。Phase 1，以及 Phase 2 的回退、stdin 审批和扩展表单链路已完成；升级整体验收尚未完成，`releaseReady=false`，不能据此发布生产。

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

- 最近已结束的 CI 基线：e5c175f，包含 stdin 真实审批与 Control/intent 锁顺序修复；常规 CI 36318253525 全绿。协议 CI 36318253522 的 Linux 与 macOS 14 均只剩 MCP017（分页遗漏）和 REVIEW006（缺少 turn/started）失败，Control-runtime 和 macOS loopback 成功。
- 最新完整覆盖基线为 e5c175f 的 Linux CI：157 项缺口（runId 06c15c5e-06e4-4f88-991b-36f90658d6c4）。这是 CI 原始 inventory 结果，替代此前 324501c 的 200 项；本地 control-only 子集不用于重算整个项目的缺口数。
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

1. 新增表单改动的完整 make ci-local 和本地 Control 矩阵通过，仍需新提交的两平台 CI 及最终完整协议覆盖；events、approvals 等已完成的专项不能等同完整覆盖通过。
2. `thread/queue/start` 已分类 controlled，但 Controller 仍需接入真实生命周期；Claude 新接口仍需实现及执行证据，不能将分类当成已实现。
3. writeStdin 已在本地及 CI 真实链路完成；Go 侧 openaiForm 归一化、扩展能力协商及本地 Control 组合通过，仍需新提交的两平台 CI。用户身份验证的可信完成、拒绝及取消链路仍未完成。
4. 新增 ThreadItem、HookMetadata、图片 fileId、异步问题等需要完整跨端行为验收，当前类型检查通过不等于能力验收。
5. MCP019 在本地和最新两平台 CI 真实通过；MCP017、REVIEW006 在 e5c175f 的两平台 CI 仍失败，不制作自编译 CLI。
6. Android 构建与模拟器错峰，排除 device offline 后复现 database is locked；iOS 继续暂缓。
7. 保留原交接中的语义专项和未执行方法缺口，完成后再考虑生产门禁。

官方协议参考：[Codex App Server](https://learn.chatgpt.com/docs/app-server)。
本次具体接口以固定 CLI 生成的 schema 为准。
