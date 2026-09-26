package appserverhub

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThreadItemsQueryRejectsInvalidParameters(t *testing.T) {
	for _, input := range []string{
		"null", "[]", "{}", "{\"threadId\":1}", "{\"threadId\":\" \"}",
		"{\"threadId\":\"t\",\"turnId\":\"\"}", "{\"threadId\":\"t\",\"turnId\":1}",
		"{\"threadId\":\"t\",\"limit\":0}", "{\"threadId\":\"t\",\"limit\":1001}",
		"{\"threadId\":\"t\",\"limit\":-1}", "{\"threadId\":\"t\",\"limit\":1.5}",
		"{\"threadId\":\"t\",\"limit\":\"1\"}", "{\"threadId\":\"t\",\"sortDirection\":\"sideways\"}",
		"{\"threadId\":\"t\",\"cursor\":\"\"}", "{\"threadId\":\"t\",\"cursor\":true}",
	} {
		_, _, err := decodeThreadItemsQuery(json.RawMessage(input))
		var protocolErr *ProtocolError
		require.ErrorAs(t, err, &protocolErr, input)
		require.Equal(t, -32602, protocolErr.Code)
	}
	query, cursor, err := decodeThreadItemsQuery(json.RawMessage("{\"threadId\":\"t\",\"turnId\":null,\"cursor\":null,\"limit\":null,\"sortDirection\":null}"))
	require.NoError(t, err)
	require.Nil(t, cursor)
	require.Equal(t, "asc", *query.SortDirection)
}

func TestThreadItemsCursorBindsQueryAndPreservesRawContent(t *testing.T) {
	input := map[string]any{"threadId": "thread", "turnId": "turn", "limit": 1}
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	query, _, err := decodeThreadItemsQuery(raw)
	require.NoError(t, err)
	large := strings.Repeat("原始工具输出\n", 8192)
	first, err := json.Marshal(map[string]any{"id": "one", "type": "commandExecution", "aggregatedOutput": large})
	require.NoError(t, err)
	second := json.RawMessage("{\"id\":\"two\",\"type\":\"agentMessage\",\"text\":\"done\"}")
	entries := []threadItemEntry{{TurnID: "turn", Item: first}, {TurnID: "turn", Item: second}}
	body, err := paginateThreadItems(query, nil, entries)
	require.NoError(t, err)
	var page struct {
		Data                        []threadItemEntry
		NextCursor, BackwardsCursor *string
	}
	require.NoError(t, json.Unmarshal(body, &page))
	require.Len(t, page.Data, 1)
	require.JSONEq(t, string(first), string(page.Data[0].Item), "不能截断完整工具输出")
	require.NotNil(t, page.NextCursor)
	require.NotNil(t, page.BackwardsCursor)
	for _, change := range []map[string]any{{"threadId": "other"}, {"turnId": "other"}, {"sortDirection": "desc"}} {
		changed := map[string]any{"threadId": "thread", "turnId": "turn", "cursor": *page.NextCursor}
		for key, value := range change {
			changed[key] = value
		}
		raw, err = json.Marshal(changed)
		require.NoError(t, err)
		_, _, err = decodeThreadItemsQuery(raw)
		var protocolErr *ProtocolError
		require.ErrorAs(t, err, &protocolErr)
		require.Equal(t, -32602, protocolErr.Code)
	}
	input["cursor"] = *page.NextCursor
	raw, err = json.Marshal(input)
	require.NoError(t, err)
	query, cursor, err := decodeThreadItemsQuery(raw)
	require.NoError(t, err)
	body, err = paginateThreadItems(query, cursor, entries)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &page))
	require.Len(t, page.Data, 1)
	require.JSONEq(t, string(second), string(page.Data[0].Item))
	require.Nil(t, page.NextCursor)
	_, err = paginateThreadItems(query, cursor, []threadItemEntry{entries[1]})
	var protocolErr *ProtocolError
	require.ErrorAs(t, err, &protocolErr)
	require.Equal(t, -32602, protocolErr.Code, "已删除锚点不能从第一页静默重来")
	invalid := base64.RawURLEncoding.EncodeToString([]byte("{}"))
	input["cursor"] = invalid
	raw, err = json.Marshal(input)
	require.NoError(t, err)
	_, _, err = decodeThreadItemsQuery(raw)
	require.ErrorAs(t, err, &protocolErr)
}

func TestThreadItemsImplementationOnlyUsesVerifiedCodexIdentity(t *testing.T) {
	require.False(t, (&Hub{}).usesCodexItemHistory())
	for _, engine := range []string{"claude-code", "codex", ""} {
		hub := &Hub{options: Options{RuntimeInfo: func() any { return map[string]string{"engine": engine} }}}
		require.Equal(t, engine == "codex", hub.usesCodexItemHistory())
	}
}
