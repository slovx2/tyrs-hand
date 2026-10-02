# Harness 适配器的独立边界

Claude Code、Pi 的协议转换和通用 SSH 库由
[codex-harness-adapter](https://github.com/slovx2/codex-harness-adapter) 维护。
本体保留 Control、Worker 注册、Hub、多端同步、业务授权、浏览器代理和部署。

Worker 在 `internal/hostworker/ssh_server.go` 中向通用 SSH 库注入授权表、
引擎环境、Hub 连接、业务命令与 OAuth 转发策略。独立本地入口仅监听
`127.0.0.1`，不依赖 Worker，也不开放通用端口转发。

## 固定消费

- `go.mod` 固定 SSH 库版本，`go.sum` 验证 Go 模块内容。
- `protocol/adapter-lock.json` 固定适配器提交、依赖组合、发布制品 URL 和 SHA-256。
- 流水线检出固定提交；`tools/adapter-build.mjs` 验证提交、版本及运行包校验值。
- 发布运行包由独立仓库构建。本体只下载、校验和签名，不维护第二套打包规则。
- 本地修改验收使用 `--local-acceptance` 调用适配器的构建入口；该运行包不能替代锁定制品。
- 集成测试源码默认位于本仓库 `adapter-source/`，也可由 `TYRS_HAND_ADAPTER_ROOT`
  显式指定。不自动读取相邻项目目录。

协议 schema、适配器扩展与 SDK 单元测试在独立仓库维护；本体保留自己的业务协议、
协议消费快照和跨引擎集成测试。`runtime/info` 的引擎信息由适配器返回，
Worker 身份和部署状态由本体组合。

## 数据格式变化

新的 Worker 适配器状态目录为
`<WorkerDataRoot>/codex-harness-adapter/{claude-code,pi}/`。
原生 Claude/Pi 配置与会话继续使用原生约定。

本次不迁移旧适配数据库，不保留旧配置别名，不读取旧 `tyrs-*` 投影标记。
显式指定旧数据库会报错；初始化和退出清理不删除旧数据库或原生会话。
这属于适配器内部数据格式变化，不改变 Codex 协议版本。

首次运行会生成新入口 HostKey，客户端需要按新指纹重新建立信任。
已有部署可在运维时显式保留原 HostKey，以维持 SSH 身份；这不涉及适配数据库或会话迁移。
旧状态目录由使用者自行保留；部署独立执行，不随代码更新自动触发。
