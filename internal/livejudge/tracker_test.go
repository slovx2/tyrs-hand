package livejudge

import (
	"errors"
	"strings"
	"testing"
)

func activeTracker() *Tracker {
	t := NewTracker("正在重构登录")
	t.Prepare(Event{Kind: "voice", ID: "v1", Text: "检查 Worker"})
	t.Prepare(Event{Kind: "applied", ID: "v1", Run: "r", Turn: "t", Sequence: 10})
	return t
}
func message(id string, seq int64) Event {
	return Event{Kind: "message", ID: id, Run: "r", Turn: "t", Sequence: seq, Text: "Worker 在线"}
}
func TestTrackerBoundaries(t *testing.T) {
	tr := activeTracker()
	for _, e := range []Event{message("old", 9), {Kind: "message", ID: "oldstart", Run: "r", Turn: "t", Sequence: 11, StartedSequence: 9, Text: "旧消息"}, {Kind: "turn_end", Run: "other", Turn: "t", Sequence: 12}, {Kind: "turn_end", Run: "r", Turn: "old", Sequence: 12}, {Kind: "delta", Run: "r", Turn: "t", Sequence: 11, Text: "a"}, {Kind: "reasoning", Run: "r", Turn: "t", Sequence: 11, Text: "a"}, {Kind: "tool", Run: "r", Turn: "t", Sequence: 11, Text: "a"}} {
		if ticket, a := tr.Prepare(e); ticket != nil || a.Closed || a.Called {
			t.Fatalf("错误处理 %+v", e)
		}
	}
	e := message("m", 13)
	ticket, _ := tr.Prepare(e)
	if ticket == nil {
		t.Fatal("未判断完整消息")
	}
	if tk, a := tr.Prepare(message("next", 14)); tk != nil || a.Ignored != "busy" {
		t.Fatal("未阻止并发乱序")
	}
	tr.Commit(ticket, Labels{true, true, false, false}, nil)
	if tk, a := tr.Prepare(e); tk != nil || a.Ignored != "duplicate" {
		t.Fatal("重复调用")
	}
	if tk, _ := tr.Prepare(message("late", 12)); tk != nil {
		t.Fatal("处理了延迟旧事件")
	}
	ticket, _ = tr.Prepare(message("next", 14))
	a := tr.Commit(ticket, Labels{false, true, true, false}, nil)
	if a.Closed || a.Speak {
		t.Fatal("无关完成误关闭")
	}
	ticket, _ = tr.Prepare(message("finish", 15))
	a = tr.Commit(ticket, Labels{true, false, true, false}, nil)
	if !a.Speak || !a.Closed {
		t.Fatal("完成结果必须播报关闭")
	}
	if tk, a := tr.Prepare(message("after", 16)); tk != nil || a.Called {
		t.Fatal("关闭后调用")
	}
}
func TestSupersededAndPending(t *testing.T) {
	tr := activeTracker()
	ticket, _ := tr.Prepare(message("m", 11))
	tr.Prepare(Event{Kind: "voice", ID: "v2", Text: "也检查队列"})
	if a := tr.Commit(ticket, Labels{true, true, true, false}, nil); a.Speak || a.Closed || a.Ignored != "stale_result" {
		t.Fatal("旧结果生效")
	}
	if tk, a := tr.Prepare(Event{Kind: "turn_end", Run: "r", Turn: "t", Sequence: 12}); tk != nil || a.Closed {
		t.Fatal("pending 被旧 turn 关闭")
	}
	tr.Prepare(Event{Kind: "applied", ID: "v1", Run: "r", Turn: "t", Sequence: 12})
	if !tr.State.Pending {
		t.Fatal("旧应用确认生效")
	}
	tr.Prepare(Event{Kind: "applied", ID: "v2", Run: "r", Turn: "t", Sequence: 12})
	ticket, _ = tr.Prepare(message("new", 13))
	if len(ticket.Input.Requests) != 2 {
		t.Fatal("未合并未完成诉求")
	}
	tr.Prepare(Event{Kind: "transfer", Text: "新会话"})
	if a := tr.Commit(ticket, Labels{true, true, true, false}, nil); a.Speak || a.Closed {
		t.Fatal("转接后旧结果生效")
	}
	if len(tr.State.Requests) != 0 || len(tr.State.Previous) != 0 {
		t.Fatal("转接泄漏上下文")
	}
}
func TestFaultsFinalAndRestart(t *testing.T) {
	tr := activeTracker()
	fault := errors.New("jev_timeout")
	for i := int64(11); i <= 12; i++ {
		ticket, _ := tr.Prepare(message(string(rune(i)), i))
		a := tr.Commit(ticket, Labels{}, fault)
		if a.Speak || a.Closed || a.FaultNotice != (i == 11) {
			t.Fatalf("%+v", a)
		}
	}
	ticket, _ := tr.Prepare(message("recovery", 13))
	tr.Commit(ticket, Labels{true, true, false, false}, nil)
	tr, err := Restore(tr.Save())
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ = tr.Prepare(message("timeout", 14))
	a := tr.Commit(ticket, Labels{}, fault)
	if !a.FaultNotice {
		t.Fatal("恢复后应重新提示故障")
	}
	e := message("final", 15)
	e.Phase = "final_answer"
	ticket, _ = tr.Prepare(e)
	a = tr.Commit(ticket, Labels{}, fault)
	if !a.Closed || a.Speak {
		t.Fatal("故障时 final 仍须关闭且不误播")
	}
	tr.Prepare(Event{Kind: "voice", ID: "v2", Text: "继续"})
	tr.Prepare(Event{Kind: "applied", ID: "v2", Run: "r", Turn: "t", Sequence: 20})
	ticket, _ = tr.Prepare(message("resume", 21))
	if len(ticket.Input.Previous) == 0 {
		t.Fatal("缺失前轮指代上下文")
	}
	tr, err = Restore(tr.Save())
	if err != nil {
		t.Fatal(err)
	}
	if tk, _ := tr.Prepare(message("resume", 21)); tk == nil {
		t.Fatal("重启丢失未提交消息")
	}
}
func TestQuestionAndHardEnd(t *testing.T) {
	tr := activeTracker()
	ticket, _ := tr.Prepare(message("q", 11))
	a := tr.Commit(ticket, Labels{true, false, false, true}, nil)
	if !a.Closed || !a.Speak || a.Reason != "needs_user_input" {
		t.Fatal("等待用户不应记作成功")
	}
	tr = activeTracker()
	_, a = tr.Prepare(Event{Kind: "turn_end", Run: "r", Turn: "t", Sequence: 11})
	if !a.Closed || a.Called {
		t.Fatal("无文本 turn 结束")
	}
	tr = activeTracker()
	_, a = tr.Prepare(Event{Kind: "hangup"})
	if !a.Closed {
		t.Fatal("挂断未关闭")
	}
	if tk, _ := tr.Prepare(message("late", 11)); tk != nil {
		t.Fatal("挂断后调用")
	}
	tr = activeTracker()
	e := message("empty-final", 11)
	e.Text = " "
	e.Phase = "final_answer"
	if tk, a := tr.Prepare(e); tk != nil || !a.Closed || a.Called {
		t.Fatal("空 final 也必须关闭")
	}
}
func TestHistoryKeepsCurrentComplete(t *testing.T) {
	tr := activeTracker()
	tr.State.History = []ContextMessage{{Text: strings.Repeat("旧", 6001)}, {Text: "最近信息"}}
	e := message("long", 11)
	e.Text = strings.Repeat("正文", 1000) + "末尾结果"
	ticket, _ := tr.Prepare(e)
	if ticket.Input.Current != e.Text || len(ticket.Input.History) != 1 || ticket.Input.History[0].Text != "最近信息" {
		t.Fatal("历史截断错误")
	}
}

func TestFinalQuestionRetainsWaitingReason(t *testing.T) {
	tr := activeTracker()
	e := message("final-q", 11)
	e.Phase = "final_answer"
	ticket, _ := tr.Prepare(e)
	a := tr.Commit(ticket, Labels{true, true, false, true}, nil)
	if !a.Closed || !a.Speak || a.Reason != "needs_user_input" {
		t.Fatalf("%+v", a)
	}
}

func TestRoleAwareUserContext(t *testing.T) {
	tr := activeTracker()
	u := Event{Kind: "user", ID: "u1", Run: "r", Turn: "t", Sequence: 11, Text: "第一台叫青杉。"}
	if ticket, a := tr.Prepare(u); ticket != nil || a.Called || a.Speak {
		t.Fatal("普通用户文本不应触发判断或播报")
	}
	ticket, _ := tr.Prepare(message("a1", 12))
	if len(ticket.Input.History) != 2 || ticket.Input.History[0].Source != "voice" || ticket.Input.History[1].Text != u.Text || ticket.Input.History[1].Role != "user" {
		t.Fatalf("缺失用户上下文: %+v", ticket.Input.History)
	}
	tr.Commit(ticket, Labels{true, true, false, true}, nil)
	u.ID = "u2"
	u.Sequence = 13
	u.Text = "青杉也是北区节点。"
	if tk, a := tr.Prepare(u); tk != nil || a.Called {
		t.Fatal("普通用户文本重新开启了跟踪")
	}
	tr.Prepare(Event{Kind: "voice", ID: "v2", Text: "继续，用北区节点。", Sequence: 14})
	tr.Prepare(Event{Kind: "applied", ID: "v2", Run: "r", Turn: "t", Sequence: 14})
	ticket, _ = tr.Prepare(message("a2", 15))
	if !strings.Contains(strings.Join(ticket.Input.Previous, "\n"), "北区节点") {
		t.Fatal("关闭后的用户解释未进入前轮上下文")
	}
	if len(ticket.Input.History) != 1 || ticket.Input.History[0].Role != "user" {
		t.Fatal("新轮次用户消息时间顺序错误")
	}
}
