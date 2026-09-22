package livejudge

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type ScorePoint struct {
	Scenario string  `json:"scenario"`
	Index    int     `json:"index"`
	Positive bool    `json:"positive"`
	Score    float64 `json:"score"`
}
type ScoreSummary struct {
	Count                      int `json:"count"`
	Min, P10, Median, P90, Max float64
	AtOrBelow06                int `json:"at_or_below_0_6"`
}
type ScoreCurve struct {
	Threshold         float64 `json:"threshold"`
	TP, FP, FN        int
	Precision, Recall float64
}
type ScoreDistribution struct {
	Prompt             string `json:"prompt"`
	Axis               string `json:"axis"`
	Round              int    `json:"round"`
	Positive, Negative ScoreSummary
	Points             []ScorePoint `json:"points"`
	Curve              []ScoreCurve `json:"curve"`
}

// 仅用于分布诊断，不是校准概率，也不替代四个独立阈值的闭环决定。
func NotificationScore(p Probabilities) float64 {
	return math.Min(p.Related, math.Max(p.Notify, math.Max(p.Completed, p.NeedsInput)))
}

func scoreSummary(values []float64) ScoreSummary {
	if len(values) == 0 {
		return ScoreSummary{}
	}
	sort.Float64s(values)
	s := ScoreSummary{Count: len(values), Min: values[0], P10: values[(len(values)-1)/10], Median: values[(len(values)-1)/2], P90: values[9*(len(values)-1)/10], Max: values[len(values)-1]}
	for _, v := range values {
		if v <= .6 {
			s.AtOrBelow06++
		}
	}
	return s
}

func ScoreDistributions(runs []Run) []ScoreDistribution {
	var out []ScoreDistribution
	for _, run := range runs {
		if run.Mode != "single" {
			continue
		} // 闭环提前关闭不能删减分布中的困难正样本。
		for axisIndex, axis := range []string{"related", "notify", "completed", "needs_user_input", "notification_gate"} {
			d := ScoreDistribution{Prompt: run.Prompt, Axis: axis, Round: run.Round}
			var positive, negative []float64
			for _, r := range run.Rows {
				if !r.Expected.Evaluate || !r.Action.Called || r.Error != "" {
					continue
				}
				p := r.Result.Probabilities
				l := r.Expected.Labels
				score := []float64{p.Related, p.Notify, p.Completed, p.NeedsInput, NotificationScore(p)}[axisIndex]
				label := []bool{l.Related, l.Notify, l.Completed, l.NeedsInput, r.Expected.Speak}[axisIndex]
				d.Points = append(d.Points, ScorePoint{r.Scenario, r.Index, label, score})
				if label {
					positive = append(positive, score)
				} else {
					negative = append(negative, score)
				}
			}
			d.Positive = scoreSummary(positive)
			d.Negative = scoreSummary(negative)
			for i := 12; i <= 19; i++ {
				c := ScoreCurve{Threshold: float64(i) / 20}
				for _, p := range d.Points {
					if p.Score >= c.Threshold {
						if p.Positive {
							c.TP++
						} else {
							c.FP++
						}
					} else if p.Positive {
						c.FN++
					}
				}
				c.Precision = ratio(c.TP, c.TP+c.FP)
				c.Recall = ratio(c.TP, c.TP+c.FN)
				d.Curve = append(d.Curve, c)
			}
			out = append(out, d)
		}
	}
	return out
}

func DistributionMarkdown(distributions []ScoreDistribution) string {
	var b strings.Builder
	b.WriteString("# 正负样本分数分布\n\n仅取单消息模式成功调用，避免闭环提前关闭把难样本从分布中移除。≤0.60 的正样本单列；通知阈值候选从 0.65 开始。\n\n`notification_gate = min(related, max(notify, completed, needs_user_input))` 是为展示‘相关且有进展/结果/提问’构造的分离指标，不是 Jev 直接返回的概率，也不是校准正确率。原始四轴另列；真实闭环继续采用四轴独立阈值，特别是结束阈值不能随通知阈值一起降低。\n\n")
	b.WriteString("|提示词/轮次|轴|正样本数|正 min/P10/median/P90/max|正≤0.6|负样本数|负 min/P10/median/P90/max|负>0.6|\n|---|---|---:|---|---:|---:|---|---:|\n")
	for _, d := range distributions {
		p, n := d.Positive, d.Negative
		fmt.Fprintf(&b, "|%s/%d|%s|%d|%.2f/%.2f/%.2f/%.2f/%.2f|%d|%d|%.2f/%.2f/%.2f/%.2f/%.2f|%d|\n", d.Prompt, d.Round, d.Axis, p.Count, p.Min, p.P10, p.Median, p.P90, p.Max, p.AtOrBelow06, n.Count, n.Min, n.P10, n.Median, n.P90, n.Max, n.Count-n.AtOrBelow06)
	}
	for _, d := range distributions {
		if d.Axis != "notify" && d.Axis != "notification_gate" {
			continue
		}
		fmt.Fprintf(&b, "\n## %s/%d · %s\n\n|阈值|TP|FP|FN|精确率|召回率|\n|---|---:|---:|---:|---:|---:|\n", d.Prompt, d.Round, d.Axis)
		for _, c := range d.Curve {
			fmt.Fprintf(&b, "|%.2f|%d|%d|%d|%.1f%%|%.1f%%|\n", c.Threshold, c.TP, c.FP, c.FN, 100*c.Precision, 100*c.Recall)
		}
	}
	return b.String()
}
