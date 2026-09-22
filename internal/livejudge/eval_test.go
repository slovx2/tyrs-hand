package livejudge

import (
	"context"
	"testing"
)

type fixedJudge struct{ p Probabilities }

func (j fixedJudge) Evaluate(context.Context, Input) (Result, error) {
	return Result{Probabilities: j.p}, nil
}

func TestCorpusGoldenTrajectories(t *testing.T) {
	d := Dataset()
	if len(d) != 40 {
		t.Fatalf("场景数 %d", len(d))
	}
	counts := map[string]int{}
	families := map[string]string{}
	messages := 0
	ids := map[string]bool{}
	for _, s := range d {
		if ids[s.ID] {
			t.Fatal("重复场景")
		}
		ids[s.ID] = true
		counts[s.Split]++
		if old, ok := families[s.Family]; ok && old != s.Split {
			t.Fatal("场景族泄漏")
		}
		families[s.Family] = s.Split
		tr := NewTracker(s.Background)
		for i, step := range s.Steps {
			if step.Event.Kind == "message" {
				messages++
			}
			ticket, a := tr.Prepare(step.Event)
			if (ticket != nil) != step.Expected.Evaluate {
				t.Fatalf("%s/%d evaluate got %v expected %v (%s)", s.ID, i, ticket != nil, step.Expected.Evaluate, a.Ignored)
			}
			if ticket != nil {
				a = tr.Commit(ticket, step.Expected.Labels, nil)
			}
			if a.Speak != step.Expected.Speak || a.Closed != step.Expected.Close {
				t.Fatalf("%s/%d action %+v expected %+v", s.ID, i, a, step.Expected)
			}
		}
	}
	if counts["dev"] != 20 || counts["holdout"] != 20 || messages < 240 {
		t.Fatalf("语料规模 %v messages=%d", counts, messages)
	}
}

func TestClosedLoopRetainsMissedMessages(t *testing.T) {
	s := scenario("test", "test", "dev", "", "修复问题", "a收到", "p发现原因", "c已修复")
	rows := Replay(context.Background(), []Scenario{s}, fixedJudge{Probabilities{1, 1, 1, 0}}, Thresholds{.5, .5, .5, .5}, "loop", 1, nil)
	m := Summarize(rows)
	if m.Calls != 1 || m.EarlyClose != 1 || m.Missed != 2 || m.CriticalTotal != 1 || m.CriticalHit != 0 || m.ProgressTotal != 1 || m.SemanticTotal != 1 || m.SemanticHit != 0 {
		t.Fatalf("提前关后的漏播被丢弃: %+v", m)
	}
}

func TestSingleMessageUsesOnlyPast(t *testing.T) {
	s := scenario("test", "test", "dev", "", "修复问题", "a收到", "p发现原因", "c已修复")
	rows := Replay(context.Background(), []Scenario{s}, fixedJudge{Probabilities{1, 1, 1, 0}}, Thresholds{.5, .5, .5, .5}, "single", 1, nil)
	if len(rows[2].Input.History) != 1 || rows[2].Input.History[0].Role != "user" || len(rows[3].Input.History) != 2 || rows[3].Input.History[1].Text != "收到" || rows[3].Input.History[1].Role != "assistant" || len(rows[3].Input.Notified) != 0 || len(rows[4].Input.Notified) != 1 {
		t.Fatal("单消息轨迹包含未来或预测内容")
	}
	if Summarize(rows).ProtocolExtraCalls != 0 {
		t.Fatal("金标轨迹不应按预测关闭来统计调用违规")
	}
}

func TestMetricsDetectIllegalCalls(t *testing.T) {
	rows := []Row{
		{Scenario: "s", Event: Event{Kind: "applied"}},
		{Scenario: "s", Event: Event{Kind: "message", ID: "m"}, Action: Action{Called: true, Closed: true}},
		{Scenario: "s", Event: Event{Kind: "message", ID: "m"}, Action: Action{Called: true}},
	}
	m := Summarize(rows)
	if m.ClosedExtraCalls != 1 || m.DuplicateExtraCalls != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestNotificationTradeoff(t *testing.T) {
	a := Metrics{SpeakTotal: 51, SpeakHit: 50, ProgressTotal: 50, ProgressHit: 50}
	b := Metrics{SpeakTotal: 45, SpeakHit: 45, ProgressTotal: 50, ProgressHit: 45}
	if !BetterNotification(a, b) {
		t.Fatal("精确率达到 98% 后应优先保留有效进展")
	}
	a.SpeakTotal = 55
	if BetterNotification(a, b) {
		t.Fatal("不能无视误播比例")
	}
}

func TestScoreDistributionKeepsRawAndDerivedSeparate(t *testing.T) {
	r := Row{Scenario: "s", Expected: Expected{Evaluate: true, Labels: Labels{Related: true, Notify: true, NeedsInput: true}, Speak: true}, Action: Action{Called: true}, Result: Result{Probabilities: Probabilities{.9, .55, .1, .95}}}
	d := ScoreDistributions([]Run{{Prompt: "test", Mode: "single", Rows: []Row{r}}})
	if len(d) != 5 || d[1].Positive.AtOrBelow06 != 1 || d[4].Positive.AtOrBelow06 != 0 || d[4].Positive.Min != .9 {
		t.Fatal("原始 notify 低分不能被合成分数掩盖")
	}
}
