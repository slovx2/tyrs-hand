# 移动端双引擎真实验收

默认 suite 使用一个正式 Worker、一个 Control、两个真实 SSH 入口。Codex 和 Claude
使用同一临时客户端密钥、不同 Host Key，访问同一个临时 Git 项目。GUI 通过正式
客户端添加 SSH、浏览 SFTP、添加项目并创建会话；不会预置会话或模型历史。

## 运行入口

- `make test-mobile-runtime-e2e`：不启动 GUI，使用真实 SSH 驱动六个场景，输出 JUnit。
- `make client-e2e-dual-engine PLATFORM=android`：构建并安装到唯一在线 Android 模拟器。
- `make client-e2e-dual-engine PLATFORM=ios`：构建并安装到唯一启动的 iPhone 模拟器。
- `make client-e2e-contract`：测试基础设施、流程 ID 和证据门禁。
- GitHub `Mobile E2E` 默认运行 `both`。只运行单平台可用于定位失败，但完整门禁不会通过。

工具链由 `protocol/adapter-lock.json` 固定。需要对应 Node、Codex、已提交且干净的
适配器，以及 Maestro 2.3.0。适配器通过 `TYRS_HAND_ADAPTER_ROOT` 指定，默认是并列
目录 `../claude-codex`；测试会检查 commit，不接受浮动安装的 Claude。

本地默认用固定 digest 的 PostgreSQL 和 Redis 容器。macOS CI 使用
`bash tools/mobile-e2e/install-native-services.sh` 安装到仓库的 `.local/mobile-services`，
设置 `TYRS_HAND_E2E_NATIVE_SERVICES=1`，将脚本输出的 bin 路径加入 PATH。源码版本、
SHA256、OpenSSL 以及 pgcrypto 均固定。缺失依赖直接失败。

## 六个必需场景

| 标识 | GUI 行为与实际断言 |
| --- | --- |
| `MOBILE_CODEX_CHAT` | Codex 入口创建会话，真实 Responses 请求和回复可见 |
| `MOBILE_CLAUDE_CHAT` | 同 Worker 的 Claude 入口创建独立会话，真实 Messages 请求和回复可见 |
| `MOBILE_CLAUDE_FULL` | 选择完全访问，原生 Write 直接写文件，无审批回调 |
| `MOBILE_CLAUDE_APPROVAL` | AI 请求文件审批，点击允许，文件落盘且结果回到模型上下文 |
| `MOBILE_CLAUDE_DENY` | 点击拒绝，不产生文件，拒绝结果回到模型上下文 |
| `MOBILE_CLAUDE_PLAN` | 进入计划，回答问题，看到计划，确认退出并执行，文件内容是选择的答案 |

结束后重启客户端，两个运行时入口仍须存在。Mock LLM 检查模型请求的引擎、上下文和
工具结果；最终核对真实文件，不能用“工具返回成功”代替副作用断言。标题请求单独识别，
不会当作聊天场景完成。未安排的请求立即失败。

## 隔离与采证

Worker 使用临时 HOME、配置、项目和虚拟密钥，清空宿主环境。macOS 用 sandbox-exec
限制外连；Linux 用独立网络 namespace，只开放 loopback，以及指向测试 Control、
Mock LLM 和 SSH 端口的固定 Unix 中继。不会读取个人模型登录态或连接公网模型服务。

`record-runtime.mjs` 在 Worker 与真实运行时的 Unix Socket 之间原样转发 WebSocket
字节，用独立副本旁路解码。它不生成协议响应。记录失败会使验收失败，不能静默缺证据。
请求、响应、回调和通知按真实方向、连接和 ID 与固定 schema 匹配；未完成请求或回调
也会阻止通过。

证据位于 `.artifacts/mobile-runtime` 和 `.local/e2e/evidence`，包含：

- JUnit、失败截图、Maestro 日志、Worker/Control 日志。
- 双引擎 wire trace、schema 报告、模型请求和真实副作用断言。
- Worker/适配器 commit、SDK/CLI 构建版本、CLI SHA256、安装版客户端版本。
- Control 数据库快照；配对令牌和临时私钥从文本证据中脱敏。

`verify-evidence.mjs` 要求 Android、iOS 各一份成功结果，源码提交和适配器一致、
工作区干净、六个场景完成、无 skip、无未回答回调。缺少任一平台不能显示完整通过。
该报告仅覆盖这些移动场景，始终保持 `completeProtocolMatrix:false`。它不能替代
全协议矩阵、安装版 Codex Desktop GUI、三端接力或生产 SSH 验收；发布还需这些门禁。
