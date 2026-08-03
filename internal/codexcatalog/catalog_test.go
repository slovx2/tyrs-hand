package codexcatalog

import (
	"context"
	"encoding/json"
	"testing"
)

type catalogCaller struct {
	calls []map[string]any
}

func (c *catalogCaller) Call(_ context.Context, method string, params, result any) error {
	if method != "model/list" {
		return nil
	}
	values := params.(map[string]any)
	c.calls = append(c.calls, values)
	response := `{"data":[{"id":"gpt-first","model":"gpt-first","displayName":"First",` +
		`"description":"first","supportedReasoningEfforts":[{"reasoningEffort":"future",` +
		`"description":"future"}],"defaultReasoningEffort":"future","isDefault":true,` +
		`"hidden":false}],"nextCursor":"next"}`
	if len(c.calls) == 2 {
		response = `{"data":[{"id":"gpt-second","model":"gpt-second",` +
			`"displayName":"Second","description":"second","supportedReasoningEfforts":[],` +
			`"defaultReasoningEffort":"low","isDefault":false,"hidden":false}],` +
			`"nextCursor":null}`
	}
	return json.Unmarshal([]byte(response), result)
}

func TestFetchPreservesCodexPagesAndOpaqueCapabilities(t *testing.T) {
	caller := &catalogCaller{}
	raw, err := Fetch(context.Background(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 2 || caller.calls[1]["cursor"] != "next" {
		t.Fatalf("没有按 Codex cursor 读取完整目录: %#v", caller.calls)
	}
	catalog, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Data) != 2 || catalog.Data[0].SupportedReasoningEfforts[0].ReasoningEffort != "future" {
		t.Fatalf("Codex 原生目录没有被完整保留: %#v", catalog)
	}
}
