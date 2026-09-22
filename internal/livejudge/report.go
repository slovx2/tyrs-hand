package livejudge

import (
	"fmt"
	"strings"
)

type Run struct {
	Policy     string     `json:"policy"`
	Prompt     string     `json:"prompt"`
	Thresholds Thresholds `json:"thresholds"`
	Mode       string     `json:"mode"`
	Round      int        `json:"round"`
	Rows       []Row      `json:"rows"`
	Metrics    Metrics    `json:"metrics"`
}

func Reclassify(rows []Row, t Thresholds) []Row {
	out := append([]Row(nil), rows...)
	for i := range out {
		r := &out[i]
		if r.Action.Called && r.Error == "" {
			r.Predicted = r.Result.Probabilities.Classify(t)
			r.Action = Decision(r.Event, r.Predicted, nil)
		}
	}
	return out
}
func Better(a, b Metrics) bool {
	x := func(m Metrics) []int {
		return []int{m.EarlyClose, m.CriticalTotal - m.CriticalHit, m.FalseSpeak, m.ProgressTotal - m.ProgressHit, m.SemanticTotal - m.SemanticHit, m.Errors}
	}
	return less(x(a), x(b))
}

func NotificationPriority(m Metrics) []int {
	precisionFailure := 0
	if ratio(m.SpeakHit, m.SpeakTotal) < .98 {
		precisionFailure = 1
	}
	return []int{m.EarlyClose, m.CriticalTotal - m.CriticalHit, precisionFailure, m.ProgressTotal - m.ProgressHit, m.SemanticTotal - m.SemanticHit, m.FalseSpeak, m.Errors}
}

func BetterNotification(a, b Metrics) bool {
	return less(NotificationPriority(a), NotificationPriority(b))
}

func Markdown(runs []Run) string {
	var b strings.Builder
	b.WriteString("# Jev Live 独立评测\n\n模型：`" + Model + "`。现有 Live 未接入。样例均为虚构业务。\n\n单消息使用人工轨迹的过去上下文；闭环只由预测推进，每条原始事件仍计入漏播。接口失败不进入混淆矩阵，但计入系统漏播和接口可用率。\n\n")
	b.WriteString("阈值选取：开发集每轴 0.50–0.95，步长 0.05。strict 策略穷举 10,000 组合，依次减少提前关闭、关键漏播、误播、进展漏播、语义漏关。notification 策略限制 notify 阈值至少 0.65，共 7,000 组合，依次减少提前关闭、关键漏播，优先达到 98% 播报精确率，再减少进展漏播、语义漏关和误播。最后比较四轴分类错误，同分保留首先枚举的配置。候选提示词再以真实闭环的同序指标选择。\n\n")
	b.WriteString("|提示词|模式/轮次|调用/错误|提前关|结果/提问召回|播报精确率|进展召回|语义结束召回|漏播|稳定P95 ms|通过|\n|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, run := range runs {
		m := run.Metrics
		fmt.Fprintf(&b, "|%s|%s/%d|%d/%d|%d|%.1f%%|%.1f%%|%.1f%%|%.1f%%|%d|%.0f|%t|\n", run.Prompt, run.Mode, run.Round, m.Calls, m.Errors, m.EarlyClose, 100*ratio(m.CriticalHit, m.CriticalTotal), 100*ratio(m.SpeakHit, m.SpeakTotal), 100*ratio(m.ProgressHit, m.ProgressTotal), 100*ratio(m.SemanticHit, m.SemanticTotal), m.Missed, m.StableP95MS, m.Passed())
	}
	for _, run := range runs {
		fmt.Fprintf(&b, "\n## %s · %s · 第 %d 轮\n\n策略 `%s`，阈值：`%+v`。输入 token %d，输出 token %d。首个记录的成功请求 %.0f ms；其余成功调用 P95 %.0f ms。超时为 3 秒，不自动重试。\n\n", run.Prompt, run.Mode, run.Round, run.Policy, run.Thresholds, run.Metrics.InputTokens, run.Metrics.OutputTokens, run.Metrics.ColdMS, run.Metrics.StableP95MS)
		b.WriteString("|轴|TP|FP|TN|FN|\n|---|---:|---:|---:|---:|\n")
		for _, key := range []string{"related", "notify", "completed", "needs_user_input"} {
			v := run.Metrics.Confusion[key]
			fmt.Fprintf(&b, "|%s|%d|%d|%d|%d|\n", key, v.TP, v.FP, v.TN, v.FN)
		}
		b.WriteString("\n|场景|调用|提前关闭|漏播|误播|\n|---|---:|---:|---:|---:|\n")
		var ids []string
		group := map[string][]Row{}
		for _, r := range run.Rows {
			if _, ok := group[r.Scenario]; !ok {
				ids = append(ids, r.Scenario)
			}
			group[r.Scenario] = append(group[r.Scenario], r)
		}
		for _, id := range ids {
			m := Summarize(group[id])
			fmt.Fprintf(&b, "|%s|%d|%d|%d|%d|\n", id, m.Calls, m.EarlyClose, m.Missed, m.FalseSpeak)
		}
		b.WriteString("\n逐条错误（完整文本、输入与概率见同目录 JSON）：\n\n")
		n := 0
		for _, r := range run.Rows {
			labelError := r.Expected.Evaluate && r.Action.Called && r.Error == "" && r.Predicted != r.Expected.Labels
			if !labelError && r.Error == "" && r.Action.Speak == r.Expected.Speak && r.Action.Closed == r.Expected.Close {
				continue
			}
			n++
			text := []rune(strings.ReplaceAll(r.Event.Text, "\n", " "))
			if len(text) > 180 {
				text = append(text[:180], []rune("…（见 JSON 完整文本）")...)
			}
			fmt.Fprintf(&b, "- `%s/%d` %s\n  - 期望播报/关闭 `%t/%t`；实际 `%t/%t`；四轴金标 `%+v`；预测 `%+v`；概率 `%+v`；错误 `%s`；跳过 `%s`。\n", r.Scenario, r.Index, string(text), r.Expected.Speak, r.Expected.Close, r.Action.Speak, r.Action.Closed, r.Expected.Labels, r.Predicted, r.Result.Probabilities, r.Error, r.Action.Ignored)
		}
		if n == 0 {
			b.WriteString("无。\n")
		}
	}
	b.WriteString("\n限制：首个请求的冷启动指标只是本进程/连接首调用，无法控制供应商模型是否冷启动；并发场景有连接池复用。有限虚构样本不能证明生产分布可靠。混合消息只评通知门控，不评 Live 是否仅口述相关部分。任何门槛未通过均不得据此移除生产标签。\n")
	return b.String()
}
