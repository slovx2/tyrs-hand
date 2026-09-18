# 移动端摘要分页验收

## 行为

- paginated 会话首屏和历史分页每次只读取 5 轮 summary；已完成轮次不请求 Item。
- 点击过程入口后正序读取 50 个 Item，更多内容由用户继续加载。运行轮次先倒序读取最新 50 个。
- 页面级 TurnDetails 保存游标、请求和展开状态；离页释放。完成后自动收起，收起不投影或解析过程 Markdown。
- FlashList 按用户消息、入口、正文块、工具操作和状态拆行。长回答按 Markdown 块延迟解析，正文不截断。
- legacy 仍读取完整轮次，但只在展开时投影过程。依赖版本和旧会话存储格式不变。

## 可重复测试

在 client 目录执行 `pnpm test`、`pnpm typecheck`、`pnpm lint`。

真实协议测试使用隔离的 Codex App Server。target.json 格式：

```json
{"url":"ws://127.0.0.1:22391","threadId":"测试会话 ID"}
```

会话需要至少 6 个已完成 Turn，最后一轮含多个 Item。设置
`TYRS_HAND_REAL_CODEX_TARGET=/absolute/path/target.json` 后执行：

```sh
pnpm exec vitest run src/app-server/officialClient.real.test.ts
```

默认只读。额外设置 `TYRS_HAND_REAL_CODEX_WRITE=true` 会在指定测试会话中发起真实模型请求，
要求模型执行 52 次独立 shell 工具调用并返回验收标记；需要该服务已配置模型和工具权限。
普通 CI 不连接外部服务，默认跳过这两个测试。

模拟器使用 Release 构建，设置 `EXPO_PUBLIC_TYRS_HAND_PREVIEW=true`、
`EXPO_PUBLIC_TYRS_HAND_PREVIEW_STRESS=true`、`EXPO_PUBLIC_TYRS_HAND_PREVIEW_PERF=true`。
运行 `preview-detail-performance.yaml` 和 `preview-summary-pagination.yaml`，
传入 `APP_ID=com.tyrshand.app`。这些压力样例仅由上述构建开关启用。

## 2026-09-18 验证结果

- 客户端 277 项测试通过；类型检查通过；Lint 0 错误，6 个已有警告。
- 本机真实 Codex 0.155.0-alpha.2.6：首屏 5 summary / 0 Item 请求，展开 1 页，
  真实 60 Item 按 50 + 10 分页；正反序一致、合并实时事件无重复或丢失、完成自动收起。
- Android Release 经测试 SSH 桥连接同一真实 Codex：摘要、展开、继续分页、收起和重新进入通过。
- 同一 Android API 35 模拟器：32 轮、500 轮分页与重进、1000 过程 Item 和 100099 字符回答流程通过。
- 1000 Item 收起首屏只有 3 行；10 万字符回答生成 120 块，首次只解析 2 个可见块。
- 10 万字符回答连续 20 次进入，数据就绪至 FlashList onLoad 的 P95 为 571.6ms
  （nearest-rank，缓存已热）。冷进入另测 363.9ms、590.8ms；**未达到 300ms 目标**。
- 同模拟器 32 轮打开的 gfxinfo：改前 117 帧、69 卡顿帧（58.97%）；改后 81 帧、
  23 卡顿帧（28.40%）；两者帧耗时 P95 均 65ms。这是单次导航样本，不代表首屏耗时或真机结论。
- 输入与返回的 100ms 目标尚未完成专门时延测量；Android 真机离线，尚未复核。

原始日志、截图和隔离测试环境保存在仓库忽略目录
`.local/e2e/evidence/20260918-mobile-detail-perf/`。
功能回归通过不代表所有性能门槛已达标；下一步需定位首屏原生布局耗时，并完成真机验收。

## 真机现场修复

- 真机确认部分已完成回答的 `phase=null`。收起状态优先显示显式 `final_answer`；
  没有显式回答时，回退显示完成态末尾的非空、null phase 助手文本。
  展开和详情分页保持该回答可见且不重复，显式 commentary 不提升为回答。
- 列表闪动来自“正在检查 Control 项目…”的插入与移除。项目检查改为依赖稳定标识和路径，
  回到前台时复查，复查期间保留上次结果，取消临时加载提示。
- 修复后 284 项测试和类型检查通过，Lint 0 错误、6 个已有警告。
  正式 Release 已覆盖安装到真机；Maestro 辅助包安装失败，真机自动回归未完成。
