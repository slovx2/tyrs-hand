package main

import (
	"fmt"
	"testing"
)

func TestTurnsPageDescendingUsesStableTurnCursor(t *testing.T) {
	thread := fixtureThreadWithTurns(8)
	first, err := turnsPage(thread, nil, 3, "desc", "full")
	if err != nil {
		t.Fatal(err)
	}
	assertTurnIDs(t, first, []string{"turn-07", "turn-06", "turn-05"})
	if first["nextCursor"] != "turn-04" {
		t.Fatalf("首个旧页游标 = %#v", first["nextCursor"])
	}

	thread.Turns = append(thread.Turns, &officialTurn{ID: "turn-08", ItemsView: "full"})
	cursor := first["nextCursor"].(string)
	second, err := turnsPage(thread, &cursor, 3, "desc", "full")
	if err != nil {
		t.Fatal(err)
	}
	assertTurnIDs(t, second, []string{"turn-04", "turn-03", "turn-02"})
	if second["nextCursor"] != "turn-01" {
		t.Fatalf("第二个旧页游标 = %#v", second["nextCursor"])
	}
}

func TestTurnsPageDefaultsToSummaryAndRejectsUnknownCursor(t *testing.T) {
	thread := fixtureThreadWithTurns(2)
	page, err := turnsPage(thread, nil, 5, "", "")
	if err != nil {
		t.Fatal(err)
	}
	assertTurnIDs(t, page, []string{"turn-01", "turn-00"})
	for _, value := range page["data"].([]any) {
		turn := value.(map[string]any)
		if turn["itemsView"] != "summary" {
			t.Fatalf("itemsView = %#v", turn["itemsView"])
		}
	}
	if page["nextCursor"] != nil {
		t.Fatalf("末页游标 = %#v", page["nextCursor"])
	}

	cursor := "missing"
	if _, err = turnsPage(thread, &cursor, 5, "desc", "full"); err == nil {
		t.Fatal("未知游标应失败")
	}
}

func fixtureThreadWithTurns(count int) *officialThread {
	thread := &officialThread{ID: "thread-1", Turns: make([]*officialTurn, 0, count)}
	for index := 0; index < count; index++ {
		thread.Turns = append(thread.Turns, &officialTurn{
			ID: fmt.Sprintf("turn-%02d", index), ItemsView: "full",
		})
	}
	return thread
}

func assertTurnIDs(t *testing.T, page map[string]any, expected []string) {
	t.Helper()
	data := page["data"].([]any)
	if len(data) != len(expected) {
		t.Fatalf("Turn 数 = %d，期望 %d", len(data), len(expected))
	}
	for index, value := range data {
		turn := value.(map[string]any)
		if turn["id"] != expected[index] {
			t.Fatalf("Turn[%d] = %#v，期望 %s", index, turn["id"], expected[index])
		}
	}
}
