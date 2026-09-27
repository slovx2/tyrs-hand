package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type revertReply struct {
	method, response string
	err              error
}

type revertRuntimeClient struct {
	t        *testing.T
	replies  []revertReply
	payloads []json.RawMessage
}

func (c *revertRuntimeClient) Call(_ context.Context, method string, params, result any) error {
	c.t.Helper()
	require.NotEmpty(c.t, c.replies, "不应发起额外原生调用: %s", method)
	reply := c.replies[0]
	c.replies = c.replies[1:]
	require.Equal(c.t, reply.method, method)
	raw, err := json.Marshal(params)
	require.NoError(c.t, err)
	c.payloads = append(c.payloads, raw)
	if reply.err != nil || result == nil {
		return reply.err
	}
	return json.Unmarshal([]byte(reply.response), result)
}

func TestResolveThreadRollbackUsesDescendingPages(t *testing.T) {
	client := &revertRuntimeClient{t: t, replies: []revertReply{
		{method: "thread/read", response: `{"thread":{"id":"t","historyMode":"paginated"}}`},
		{method: "thread/turns/list", response: `{"data":[{"id":"third"}],"nextCursor":"page2"}`},
		{method: "thread/turns/list", response: `{"data":[{"id":"second"}],"nextCursor":"page3"}`},
	}}
	params, err := ResolveThreadRollback(context.Background(), client, "t", 2)
	require.NoError(t, err)
	require.Equal(t, ThreadRevertParams{ThreadID: "t", BeforeTurnID: "second"}, params)
	require.Empty(t, client.replies)
	require.JSONEq(t, `{"threadId":"t","cursor":null,"limit":2,"sortDirection":"desc","itemsView":"notLoaded"}`, string(client.payloads[1]))
	require.JSONEq(t, `{"threadId":"t","cursor":"page2","limit":1,"sortDirection":"desc","itemsView":"notLoaded"}`, string(client.payloads[2]))
}

func TestResolveThreadRollbackRejectsWithoutMutation(t *testing.T) {
	metadata := revertReply{method: "thread/read", response: `{"thread":{"id":"t","historyMode":"paginated"}}`}
	for _, test := range []struct {
		name, threadID, want string
		count                int
		replies              []revertReply
	}{
		{name: "empty-thread", count: 1, want: "threadId"},
		{name: "zero", threadID: "t", want: "正整数"},
		{name: "negative", threadID: "t", count: -1, want: "正整数"},
		{name: "legacy", threadID: "t", count: 1, want: "legacy 线程不支持回退", replies: []revertReply{
			{method: "thread/read", response: `{"thread":{"id":"t","historyMode":"legacy"}}`},
		}},
		{name: "wrong-thread", threadID: "t", count: 1, want: "不匹配", replies: []revertReply{
			{method: "thread/read", response: `{"thread":{"id":"other","historyMode":"paginated"}}`},
		}},
		{name: "too-many", threadID: "t", count: 2, want: "超过", replies: []revertReply{metadata,
			{method: "thread/turns/list", response: `{"data":[{"id":"one"}],"nextCursor":null}`},
		}},
		{name: "duplicate-turn", threadID: "t", count: 3, want: "重复", replies: []revertReply{metadata,
			{method: "thread/turns/list", response: `{"data":[{"id":"one"},{"id":"one"}],"nextCursor":null}`},
		}},
		{name: "empty-page", threadID: "t", count: 1, want: "没有推进", replies: []revertReply{metadata,
			{method: "thread/turns/list", response: `{"data":[],"nextCursor":"same"}`},
		}},
		{name: "upstream-error", threadID: "t", count: 1, want: "read failed", replies: []revertReply{
			{method: "thread/read", err: errors.New("read failed")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &revertRuntimeClient{t: t, replies: test.replies}
			_, err := ResolveThreadRollback(context.Background(), client, test.threadID, test.count)
			require.ErrorContains(t, err, test.want)
			require.Empty(t, client.replies)
		})
	}
}
