# Codex 0.157.1 升级进度

更新时间：2026-09-27。Phase 1 和 Phase 2 的回退链路已完成；升级整体验收尚未完成，`releaseReady=false`，不能据此发布生产。

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

## Phase 2：原生回退与 Control 替换

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

1. 本轮回退改动仍需由新一轮全量 CI/matrix 验证；修复剩余 events、approvals 等旧 shell 工具夹具，保留实际事件和副作用断言。
2. `thread/queue/start` 已分类 controlled，但 Controller 仍需接入真实生命周期；Claude 新接口仍需实现及执行证据，不能将分类当成已实现。
3. `kind=writeStdin` 审批卡片和审计区分；Go 侧 openaiForm 归一化、用户身份验证的可信完成/拒绝链路。
4. 新增 ThreadItem、HookMetadata、图片 fileId、异步问题等需要完整跨端行为验收，当前类型检查通过不等于能力验收。
5. MCP019 已在本地真实关闭，需由完整 matrix 复验；MCP017、REVIEW006 仍是已知上游阻塞，本轮未再定向复测，不制作自编译 CLI。
6. Android 构建与模拟器错峰，排除 device offline 后复现 database is locked；iOS 继续暂缓。
7. 保留原交接中的语义专项和未执行方法缺口，完成后再考虑生产门禁。

官方协议参考：[Codex App Server](https://learn.chatgpt.com/docs/app-server)。
本次具体接口以固定 CLI 生成的 schema 为准。
