# Codex 0.157.1 升级进度

更新时间：2026-09-28。Phase 1，以及 Phase 2 的回退、stdin 审批和扩展表单链路已完成；升级整体验收尚未完成，`releaseReady=false`，不能据此发布生产。

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
