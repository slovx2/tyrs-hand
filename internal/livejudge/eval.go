package livejudge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type Row struct {
	Mode      string   `json:"mode"`
	Scenario  string   `json:"scenario"`
	Family    string   `json:"family"`
	Index     int      `json:"index"`
	Event     Event    `json:"event"`
	Expected  Expected `json:"expected"`
	Input     *Input   `json:"input,omitempty"`
	Result    Result   `json:"result"`
	Error     string   `json:"error,omitempty"`
	Predicted Labels   `json:"predicted"`
	Action    Action   `json:"action"`
}
type Matrix struct{ TP, FP, TN, FN int }
type Metrics struct {
	Calls, Errors, EarlyClose, Missed, FalseSpeak                                int
	CriticalTotal, CriticalHit, ProgressTotal, ProgressHit, SpeakTotal, SpeakHit int
	SemanticTotal, SemanticHit, ProtocolExtraCalls                               int
	ClosedExtraCalls, DuplicateExtraCalls                                        int
	InputTokens, OutputTokens                                                    int
	ColdMS, StableP95MS                                                          float64
	Confusion                                                                    map[string]Matrix
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}
func (m Metrics) Passed() bool {
	return m.Calls > 0 && m.EarlyClose == 0 && ratio(m.CriticalHit, m.CriticalTotal) >= .98 && ratio(m.SpeakHit, m.SpeakTotal) >= .98 && ratio(m.ProgressHit, m.ProgressTotal) >= .95 && ratio(m.SemanticHit, m.SemanticTotal) >= .95 && m.ProtocolExtraCalls == 0 && ratio(m.Calls-m.Errors, m.Calls) >= .99 && m.StableP95MS <= 2000
}
func (m Metrics) Summary() string {
	return fmt.Sprintf("calls=%d errors=%d early_close=%d critical=%.1f%% precision=%.1f%% progress=%.1f%% semantic=%.1f%% missed=%d p95=%.0fms pass=%t", m.Calls, m.Errors, m.EarlyClose, 100*ratio(m.CriticalHit, m.CriticalTotal), 100*ratio(m.SpeakHit, m.SpeakTotal), 100*ratio(m.ProgressHit, m.ProgressTotal), 100*ratio(m.SemanticHit, m.SemanticTotal), m.Missed, m.StableP95MS, m.Passed())
}

// 单消息模式使用金标轨迹构造过去上下文；闭环模式仅以预测推进状态。
// 每个原始事件都会产生 Row，因此提前停止后的漏播不会消失。
func Replay(ctx context.Context, scenarios []Scenario, judge Judge, thresholds Thresholds, mode string, concurrency int, progress func(string)) []Row {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([][]Row, len(scenarios))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < concurrency; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				s := scenarios[i]
				t := NewTracker(s.Background)
				for j, step := range s.Steps {
					r := Row{Mode: mode, Scenario: s.ID, Family: s.Family, Index: j, Event: step.Event, Expected: step.Expected}
					ticket, a := t.Prepare(step.Event)
					r.Action = a
					if ticket != nil {
						r.Input = &ticket.Input
						result, err := judge.Evaluate(ctx, ticket.Input)
						r.Result = result
						if err != nil {
							r.Error = err.Error()
						} else {
							r.Predicted = result.Probabilities.Classify(thresholds)
						}
						if mode == "single" {
							r.Action = Decision(step.Event, r.Predicted, err)
							t.Commit(ticket, step.Expected.Labels, nil)
						} else {
							r.Action = t.Commit(ticket, r.Predicted, err)
						}
					}
					results[i] = append(results[i], r)
				}
				if progress != nil {
					progress(s.ID)
				}
			}
		}()
	}
	for i := range scenarios {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var rows []Row
	for _, r := range results {
		rows = append(rows, r...)
	}
	return rows
}

func Decision(e Event, l Labels, err error) Action {
	a := Action{Called: true}
	if err == nil {
		a.Speak = l.Related && (l.Notify || l.Completed || l.NeedsInput)
		a.Closed = l.Related && (l.Completed || l.NeedsInput)
		if a.Closed {
			a.Reason = "completed"
			if l.NeedsInput {
				a.Reason = "needs_user_input"
			}
		}
	}
	if e.Phase == "final_answer" && !a.Closed {
		a.Closed = true
		a.Reason = "final_answer"
	}
	return a
}

func Summarize(rows []Row) Metrics {
	m := Metrics{Confusion: map[string]Matrix{}}
	var latency []float64
	active := map[string]bool{}
	seen := map[string]bool{}
	coldIndex := -1
	for i, r := range rows {
		if r.Action.Called && r.Error == "" && (coldIndex < 0 || (!r.Result.StartedAt.IsZero() && r.Result.StartedAt.Before(rows[coldIndex].Result.StartedAt))) {
			coldIndex = i
		}
	}
	for i, r := range rows {
		if r.Event.Kind == "voice" {
			active[r.Scenario] = false
		}
		if r.Event.Kind == "applied" && r.Action.Ignored == "" {
			active[r.Scenario] = true
		}
		if r.Action.Called {
			key := r.Scenario + "/" + r.Event.Run + "/" + r.Event.Turn + "/" + r.Event.ID
			if !active[r.Scenario] {
				m.ClosedExtraCalls++
				m.ProtocolExtraCalls++
			}
			if seen[key] {
				m.DuplicateExtraCalls++
				m.ProtocolExtraCalls++
			}
			seen[key] = true
			m.Calls++
			m.InputTokens += r.Result.InputTokens
			m.OutputTokens += r.Result.OutputTokens
			if r.Error != "" {
				m.Errors++
			} else {
				ms := float64(r.Result.Elapsed) / 1e6
				if i == coldIndex {
					m.ColdMS = ms
				} else {
					latency = append(latency, ms)
				}
			}
		}
		if (r.Mode == "single" && r.Expected.Close) || (r.Mode != "single" && r.Action.Closed) {
			active[r.Scenario] = false
		}
		if r.Action.Speak {
			m.SpeakTotal++
			if r.Expected.Speak {
				m.SpeakHit++
			} else {
				m.FalseSpeak++
			}
		}
		if r.Expected.Speak && !r.Action.Speak {
			m.Missed++
		}
		if r.Expected.Critical && r.Expected.Speak {
			m.CriticalTotal++
			if r.Action.Speak {
				m.CriticalHit++
			}
		}
		if r.Expected.Speak && !r.Expected.Critical {
			m.ProgressTotal++
			if r.Action.Speak {
				m.ProgressHit++
			}
		}
		semantic := r.Expected.Evaluate && r.Expected.Labels.Related && (r.Expected.Labels.Completed || r.Expected.Labels.NeedsInput)
		if semantic {
			m.SemanticTotal++
			if r.Action.Called && r.Error == "" && r.Predicted.Related && (r.Predicted.Completed || r.Predicted.NeedsInput) {
				m.SemanticHit++
			}
		}
		if r.Action.Closed && !r.Expected.Close && r.Expected.Evaluate {
			m.EarlyClose++
		}
		if r.Expected.Evaluate && r.Action.Called && r.Error == "" {
			want := []bool{r.Expected.Labels.Related, r.Expected.Labels.Notify, r.Expected.Labels.Completed, r.Expected.Labels.NeedsInput}
			got := []bool{r.Predicted.Related, r.Predicted.Notify, r.Predicted.Completed, r.Predicted.NeedsInput}
			for i, key := range []string{"related", "notify", "completed", "needs_user_input"} {
				v := m.Confusion[key]
				switch {
				case want[i] && got[i]:
					v.TP++
				case !want[i] && got[i]:
					v.FP++
				case want[i] && !got[i]:
					v.FN++
				default:
					v.TN++
				}
				m.Confusion[key] = v
			}
		}
	}
	sort.Float64s(latency)
	if len(latency) > 0 {
		m.StableP95MS = latency[(95*len(latency)+99)/100-1]
	}
	return m
}

// 四轴独立扫描 10^4 个组合，不重发请求。按安全优先的固定字典序选取。
func Tune(rows []Row) (Thresholds, Metrics) {
	return tune(rows, false)
}

// 通知策略允许精确率达标后的少量误播，优先提高进展召回，避免一味抬高阈值。
func TuneNotification(rows []Row) (Thresholds, Metrics) { return tune(rows, true) }

func tune(rows []Row, notification bool) (Thresholds, Metrics) {
	best := Thresholds{}
	var bestM Metrics
	var bestScore []int
	copyRows := append([]Row(nil), rows...)
	for r := 10; r <= 19; r++ {
		for n := 10; n <= 19; n++ {
			if notification && n < 13 {
				continue
			} // 用户要求通知正样本高于 0.6。
			for c := 10; c <= 19; c++ {
				for u := 10; u <= 19; u++ {
					t := Thresholds{float64(r) / 20, float64(n) / 20, float64(c) / 20, float64(u) / 20}
					for i := range copyRows {
						row := &copyRows[i]
						if row.Action.Called && row.Error == "" {
							row.Predicted = row.Result.Probabilities.Classify(t)
							row.Action = Decision(row.Event, row.Predicted, nil)
						}
					}
					m := Summarize(copyRows)
					score := []int{m.EarlyClose, m.CriticalTotal - m.CriticalHit, m.FalseSpeak, m.ProgressTotal - m.ProgressHit, m.SemanticTotal - m.SemanticHit}
					if notification {
						score = NotificationPriority(m)
					}
					for _, k := range []string{"related", "notify", "completed", "needs_user_input"} {
						v := m.Confusion[k]
						score = append(score, v.FP+v.FN)
					}
					if bestScore == nil || less(score, bestScore) {
						best, bestM, bestScore = t, m, score
					}
				}
			}
		}
	}
	return best, bestM
}
func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func EncodeDataset() []byte { b, _ := json.MarshalIndent(Dataset(), "", "  "); return b }
