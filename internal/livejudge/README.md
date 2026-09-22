# Jev 语音委托评测

独立实验模块，不调用数据库、Control 或 Live，不改变现有标签行为。

## 运行

```sh
go test -race ./internal/livejudge ./cmd/tyrs-hand-jev-eval
go vet ./internal/livejudge ./cmd/tyrs-hand-jev-eval

# 只导出已标注的数据集，无需密钥，也不发网络请求。
go run ./cmd/tyrs-hand-jev-eval -mode dataset -out .local/jev-eval/new-run

# 可省略 -env，从进程环境读取 JEV_API_KEY；指定文件中的 key 优先。
go run ./cmd/tyrs-hand-jev-eval -mode develop -policy notification -env /path/to/config.env -out .local/jev-eval/new-run
go run ./cmd/tyrs-hand-jev-eval -mode holdout -env /path/to/config.env -out .local/jev-eval/new-run

# 可选：绘图脚本及其依赖由 uv script lock 固定。
uv run --locked cmd/tyrs-hand-jev-eval/plot_scores.py .local/jev-eval/new-run/develop-scores.json
uv run --locked cmd/tyrs-hand-jev-eval/plot_scores.py .local/jev-eval/new-run/holdout-scores.json
```

默认并发三个场景，场景内逐条串行。每条完整消息一次 HTTP 请求，同时提交四个 `noul` 问题。
模型固定为 `jev-1.13.0`，每次请求超时 3 秒，无自动重试，不跟随重定向，不回显上游错误体或密钥。
`-mode holdout` 完整运行三轮单消息和闭环回放，写完报告后任何门槛未通过即返回非零退出码。

输出包括 `dataset.json`、`develop.json/.md`、`frozen.json`、`holdout.json/.md`，以及两阶段的 `*-scores.json/.md` 分数分布和阈值曲线。
JSON 逐事件保存输入、预期、概率、预测、调用耗时、token 用量、动作和错误。
这些文件只包含虚构样例及其评测结果，不含 API key。输出目录及文件分别以 0700/0600 创建。

## 冻结与复验

- 四版提示词及人工金标见 `prompts.go`、`corpus.go`、`corpus_holdout.go`。
- 开发集每轴在 0.50–0.95 中以 0.05 为步长。`strict` 穷举 10,000 组合；`notification` 要求 notify 阈值至少 0.65，穷举 7,000 组合。复用本次单消息概率，调阈值不重发请求。
- `strict` 按提前关闭、关键漏播、误播、进展漏播、语义漏关的顺序优化。用户提出看分数分布后增加 `notification` 策略：先避免提前关闭和关键漏播，达到 98% 播报精确率后优先提高进展召回，再提高语义结束召回、减少误播。同分比较四轴错误，再保留枚举顺序最前项。
- 每版提示词用选出的阈值执行真实闭环，再以同样的动作指标选择提示词。闭环输入里的已通知内容来自预测，不能复用金标轨迹的概率。
- 冻结模型版本、提示词哈希、完整数据集哈希和四轴阈值。留出运行前校验，禁止覆盖已有留出结果。
- 三轮留出期间不得修改提示词、阈值或样例。若要继续调优，须准备新的留出场景；CLI 的哈希保护不替代这项实验纪律。

单消息模式按人工金标推进历史，适合定位某一轴的错误；其通知/结束动作仍使用真实预测。
闭环模式由真实预测决定是否关闭。关闭后跳过的原始事件仍保留在报告中，计入关键结果、有效进展和语义结束的漏检。
接口失败不计作语义判断的对错，但计入系统漏播和接口可用率。概率本身不等于实测准确率。

分布报告同时显示四个原始分数和派生指标 `min(related, max(notify, completed, needs_user_input))`。
派生指标只帮助观察通知的正负分离，不能冒充 Jev 原始概率；原始 notify 正样本 ≤0.6 的数量仍单独列出。
派生指标的阈值扫描与真实四轴闭环是不同实验，必须以闭环结果验证实际效果。

首调用耗时指该轮最早发起且成功的请求，其余成功请求统计稳定 P95；无法保证供应商模型恰处于冷启动状态。
关闭后/重复额外调用根据实际动作轨迹计数；模型漏关导致继续判断则通过语义结束召回、误播和逐事件记录反映。

## 状态机适配约定

`Prepare(Event)` 和 `Commit(Ticket, Labels, error)` 将网络请求与状态锁分开。
调用方应对同一跟踪维持串行队列，遇到 `busy` 保留事件并重试；不能丢弃结束事件。
新语音、转接、挂断可以在请求途中使旧 ticket 失效。`Commit` 返回 `stale_result` 时不得发送通知或持久化旧决定。

事件：

|kind|必须提供|作用|
|---|---|---|
|voice|唯一 ID、Text|登记待应用委托，合并未完成诉求|
|applied|对应最新 voice ID、Run、Turn、Sequence|Worker 确认应用后才启用跟踪|
|message|ID、Run、Turn、Sequence、完整 Text|已完成的 agentMessage；Phase 仅 `final_answer` 强制结束|
|user|ID、Run、Turn、Sequence、完整 Text|普通用户消息，只更新上下文，不调用 Jev、不开启跟踪|
|turn_end|Run、Turn、Sequence|仅关闭当前绑定 turn|
|transfer / hangup|transfer 可带新 Background 文本|关闭并使旧请求失效；转接清空旧会话上下文|

消息的 `StartedSequence` 若可获得，应由适配器提供，用来拒绝在委托应用前开始、之后才完成的旧消息。
不应将 delta、工具输出或 reasoning 转成 `message`。所有序号都必须来自同一 run 的已落库事件顺序。
去重标识由 run/turn/message ID 组成，快照会保留去重记录；恢复时仅重试未提交的在途消息。

历史按事件顺序保留 `user/assistant` 角色、来源、消息 ID、文本和序号；包含语音原文、追加语音、普通用户文本，以及未播报或与委托无关的助手消息。关闭后同一绑定 turn 的用户补充仍可更新指代上下文，但不重新启动跟踪。
历史只丢弃最旧整条消息，当前文本不按 500 字截断。近期消息、已通知内容、前轮上下文分别限制为 6000、4000、4000 个字符。
请求原文与当前文本保持完整；总 state 超过 24000 字符时明确失败，保留跟踪，避免静默丢掉末尾结果。
这是一项保守的字符预算，并非精确 tokenizer 计量；超长输入的生产策略仍需接入阶段设计。

`needs_user_input` 的结束原因优先保留为等待用户；不是工作成功。无关 final 不播但硬结束，空 final 也直接结束。
连续调用故障只发一次 `FaultNotice`，成功后重置；协议 final/turn_end 在故障时仍然结束。

## 后续接入边界

本模块没有生产持久化、事件消费者、Live 摘要/播报或断线恢复适配器。
即使语义评测通过，仍须单独实现 Control 持久化与异步队列，并验证 Live 对混合消息只口述相关部分。
生产接入还要同时移除 Worker 标签提示和 Live 标签过滤，不能用双轨兼容掩盖评测失败。

API 和 `noul` 协议依据：[TypeSafe API 文档](https://docs.typesafe.ai/api)；模型版本依据：[模型文档](https://docs.typesafe.ai/models)。
