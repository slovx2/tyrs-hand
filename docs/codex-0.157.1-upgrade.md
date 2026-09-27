# Codex 0.157.1 升级进度

更新时间：2026-09-27。当前仅完成 Phase 1，`releaseReady=false`，不能据此发布生产。

## 已完成

- CLI、CI、工具链、手机端、协议清单与适配器切换到精确版本 0.157.1。
- Worker 和手机 SSH 版本探测只接受 0.157.1，拒绝更高版本、预发布版和自定义构建后缀。
- 通过官方 CLI 重新生成 `protocol/codex-app-server/0.157.1/`，保留 0.147.0 作为升级差异基线。
- 新增请求实际为 35 个，删除 `thread/rollback` 1 个，净增 34 个；新增通知 13 个。
- 新请求已在 Hub 分类，新请求和通知已进入 runtime-matrix；两引擎均保留 required 和空执行用例，不计作覆盖通过。
- 老 Desktop 的 `thread/rollback` 已登记为显式扩展契约，响应仍校验新版 Thread。**Codex 的运行时转换尚未实现。**
- 适配器 Thread 输出必填 `projectId: null`，CLI shim、initialize 与 runtime/info 版本一致。
- 客户端测试与预览补齐新版字段，处理 functionCallOutput、新协作工具枚举和 completed 子任务活动。
- fileId 图片保留附件标识，不再假定一定有 URL；尚未实现通过 fileId 下载图片。
- 手机 MCP 表单区分 openaiForm 与用户身份验证；身份验证卡片不提供伪造成功的确认按钮，完整链路仍待实现。

适配器提交：`25bf4ee06b2daee45bc3d36e81837f6d248f0877`。

## Hub 偶发归档单测

事实：旧用例在 RPC 响应后才建立订阅，但创建通知异步分发，响应完成不代表通知已经完成分发。
该用例本地重复 100 次及带 race 重复 1000 次均未复现 CI 的失败。

推测：CI 中尚未分发的 `thread/started` 在订阅建立后到达，导致断言提前把它当成归档事件。
已让测试在创建前订阅，并显式校验创建通知，再校验归档和取消归档顺序。
本轮没有修改 Hub 的事件分发行为，也不声称已直接复现原失败。

## 本地验证

- `go test ./internal/... ./mobile/sshtransport/...` 通过。
- `go test -race ./internal/appserverhub/... ./internal/worker/... ./internal/bootstrap/...` 通过。
- golangci-lint v2.12.2：Codex、Hub、Hostworker、手机 SSH transport，含 integration build tag，通过。
- 客户端 TypeScript 类型检查通过；单测 366 通过、2 个真实连接用例按原配置跳过。
- 客户端 ESLint：0 errors，6 个既存 warnings。
- 协议 inventory 与 mobile E2E 契约测试：23/23。
- 适配器 build 与全量 npm test：338/338；Biome 无错误，仍有既存 warnings/infos。
- 新增接口的真实执行证据、全量双引擎协议 matrix、真实 SSH 接力与 Android GUI **尚未在最终组合重跑**。

## Phase 2 必须完成

1. Codex rollback → revert 转换；paginated 线程按 beforeTurnId 回退，legacy 明确拒绝。同步 Control 投影、Worker 原生能力和真实副作用测试。
2. `thread/revert` 和 `thread/queue/start` 已分类 controlled，但 Controller 仍需接入它们的真实生命周期，不能将分类当成已实现。
3. `kind=writeStdin` 审批卡片和审计区分；Go 侧 openaiForm 归一化、用户身份验证的可信完成/拒绝链路。
4. 新增 ThreadItem、HookMetadata、图片 fileId、异步问题等需要完整跨端行为验收，当前类型检查通过不等于能力验收。
5. 以 0.157.1 重跑 MCP019；MCP017、REVIEW006 仍是已知上游阻塞，不制作自编译 CLI。
6. Android 构建与模拟器错峰，排除 device offline 后复现 database is locked；iOS 继续暂缓。
7. 保留原交接中的语义专项和未执行方法缺口，完成后再考虑生产门禁。

官方协议参考：[Codex App Server](https://learn.chatgpt.com/docs/app-server)。
本次具体接口以固定 CLI 生成的 schema 为准。
