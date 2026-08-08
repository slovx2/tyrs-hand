package discordintegration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/stretchr/testify/require"
)

func TestOfficialRequestCardsAndParsers(t *testing.T) {
	id := uuid.New()
	params := json.RawMessage(`{"questions":[
		{"id":"q1","header":"Choose","question":"Which?","options":[
			{"label":"Alpha","description":"first"}],"isOther":true},
		{"id":"q2","header":"Secret","question":"Token?","isSecret":true}
	]}`)
	request := officialRequestProjection{ID: id, Status: "pending",
		Method: "item/tool/requestUserInput", Params: params,
		Draft: map[string]json.RawMessage{}}
	card := officialRequestCard(request)
	require.Contains(t, card.Header, "Choose")
	require.Len(t, card.Buttons, 2)
	require.Contains(t, card.Body, "first")

	request.Draft["q1"] = json.RawMessage(`{"answers":["Alpha"]}`)
	card = officialRequestCard(request)
	require.Contains(t, card.Header, "Secret")
	require.Contains(t, card.Body, "Mobile")
	require.Empty(t, card.Buttons)
	request.Draft["q2"] = json.RawMessage(`{"answers":["hidden"]}`)
	require.Contains(t, officialRequestCard(request).Header, "等待提交")

	for status, body := range map[string]string{
		"answered": "回答已提交", "dismissed": "跳过", "stale": "连接已断开",
		"resolved": "其他客户端",
	} {
		request.Status = status
		require.Contains(t, officialRequestCard(request).Body, body)
	}
	mcp := officialRequestProjection{ID: id, Status: "pending",
		Method: "mcpServer/elicitation/request", Params: json.RawMessage(`{"message":"input"}`)}
	require.Contains(t, officialRequestCard(mcp).Body, "input")
	approval := officialRequestProjection{ID: id, Status: "pending",
		Method: "item/commandExecution/requestApproval",
		Params: json.RawMessage(`{"command":"go test ./..."}`)}
	require.Contains(t, officialRequestCard(approval).Body, "go test")
	require.Equal(t, approval.Method, officialApprovalText(officialRequestProjection{
		Method: approval.Method, Params: json.RawMessage(`{}`)}))

	parsedID, question, option, err := parseOfficialInputButton(
		officialInputButtonPrefix + id.String() + ":2:-1")
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.Equal(t, 2, question)
	require.Equal(t, -1, option)
	for _, value := range []string{"bad", officialInputButtonPrefix + "bad:0:0",
		officialInputButtonPrefix + id.String() + ":-1:0"} {
		_, _, _, err = parseOfficialInputButton(value)
		require.Error(t, err)
	}
	parsedID, decision, err := parseOfficialApproval(
		officialApprovalPrefix + id.String() + ":accept")
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.Equal(t, "accept", decision)
	_, _, err = parseOfficialApproval(officialApprovalPrefix + id.String() + ":always")
	require.Error(t, err)
	parsedID, question, err = parseOfficialInputModal(
		officialInputModalPrefix + id.String() + ":3")
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.Equal(t, 3, question)
	_, _, err = parseOfficialInputModal(officialInputModalPrefix + id.String() + ":-1")
	require.Error(t, err)
}

func TestOfficialApprovalResponses(t *testing.T) {
	assertJSON := func(method, decision, params, expected string) {
		t.Helper()
		value, err := officialApprovalResponse(method, decision, json.RawMessage(params))
		require.NoError(t, err)
		require.JSONEq(t, expected, string(value))
	}
	assertJSON("item/commandExecution/requestApproval", "accept", `{}`,
		`{"decision":"accept"}`)
	assertJSON("item/fileChange/requestApproval", "cancel", `{}`,
		`{"decision":"decline"}`)
	assertJSON("item/permissions/requestApproval", "accept",
		`{"permissions":{"network":true,"empty":null}}`,
		`{"permissions":{"network":true},"scope":"turn"}`)
	assertJSON("item/permissions/requestApproval", "decline", `{}`,
		`{"permissions":{},"scope":"turn"}`)
	assertJSON("mcpServer/elicitation/request", "cancel", `{}`,
		`{"action":"cancel","content":null,"_meta":null}`)
	assertJSON("applyPatchApproval", "accept", `{}`, `{"decision":"approved"}`)
	assertJSON("execCommandApproval", "decline", `{}`, `{"decision":"abort"}`)
	_, err := officialApprovalResponse("mcpServer/elicitation/request", "accept", nil)
	require.Error(t, err)
	_, err = officialApprovalResponse("unknown", "decline", nil)
	require.Error(t, err)
	_, err = officialApprovalResponse("execCommandApproval", "always", nil)
	require.Error(t, err)
}

func TestOfficialItemCardsAndDesktopPagination(t *testing.T) {
	plan := &officialapp.Plan{TurnID: "turn", ItemID: "plan", Text: "steps"}
	turn := officialapp.Turn{ID: "turn", Status: "completed"}
	card, visible := officialItemCard(turn,
		officialapp.Item{Type: "plan", ID: "plan", Text: "steps"}, plan, uuid.New())
	require.True(t, visible)
	require.False(t, card.Buttons[0].Disabled)
	_, visible = officialItemCard(turn, officialapp.Item{Type: "userMessage", ID: "user"},
		plan, uuid.Nil)
	require.False(t, visible)
	card, visible = officialItemCard(turn, officialapp.Item{Type: "reasoning", ID: "why",
		Raw: json.RawMessage(`{"summary":["because"]}`)}, nil, uuid.Nil)
	require.True(t, visible)
	require.Contains(t, card.Body, "because")
	card, visible = officialItemCard(turn, officialapp.Item{Type: "commandExecution", ID: "cmd",
		Raw: json.RawMessage(`{"command":"pwd","status":"completed"}`)}, nil, uuid.Nil)
	require.True(t, visible)
	require.Contains(t, card.Body, "pwd")
	require.Equal(t, "bad name", officialItemLabel("bad_name"))

	cards := DesktopInputCards("", "")
	require.Len(t, cards, 1)
	require.Contains(t, cards[0].Header, "Desktop")
	require.Contains(t, cards[0].Body, "无文本")
	cards = DesktopInputCards("Phone", strings.Repeat("a", desktopInputPageRunes+1))
	require.Len(t, cards, 2)
	require.Contains(t, cards[0].Header, "1/2")
}

func TestProtocolStatusAndErrorCards(t *testing.T) {
	require.Contains(t, terminatedControlCard().Header, "终止")
	require.Contains(t, archivedConversationCard().Header, "归档")
	require.Equal(t, "Pull Request", taskKindLabel("pull_request"))
	require.Equal(t, "Issue", taskKindLabel("issue"))
	require.Equal(t, "custom kind", taskKindLabel("custom_kind"))
	require.Equal(t, "…", cardText("ab", 1))
	require.Equal(t, "a…", cardText("abc", 2))

	details := ComponentErrorPayload{Message: "failed", CodexErrorInfo: json.RawMessage(`"sandbox"`),
		AdditionalDetails: "details", WillRetry: true, ThreadID: "thread", TurnID: "turn"}
	sections := componentErrorSections(details)
	require.Len(t, sections, 1)
	require.Contains(t, sections[0], "sandbox")
	require.Contains(t, sections[0], "willRetry")
	details.Message = strings.Repeat("长", 4100)
	details.CodexErrorInfo = json.RawMessage(`{"kind":"structured"}`)
	require.Greater(t, len(componentErrorSections(details)), 1)
	require.Empty(t, componentErrorInfoText(nil))
	require.Empty(t, componentErrorInfoText(json.RawMessage(`null`)))
	require.Equal(t, "plain", componentErrorInfoText(json.RawMessage(`"plain"`)))
	require.Equal(t, `{"kind":"structured"}`,
		componentErrorInfoText(json.RawMessage(`{"kind":"structured"}`)))
	require.Empty(t, splitCardText("", 10))
	require.Equal(t, []string{"ab", "cd", "e"}, splitCardText("abcde", 2))
}
