package discordintegration

import (
	"encoding/base64"
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
	require.NotEmpty(t, card.Buttons[0].CustomID)
	require.NoError(t, validateOfficialCard(card))
	stale, visible := officialItemCard(turn,
		officialapp.Item{Type: "plan", ID: "old-plan", Text: "old steps"}, plan, uuid.New())
	require.True(t, visible)
	require.Empty(t, stale.Buttons, "历史 Plan Item 不应生成无效的禁用按钮")
	require.NoError(t, validateOfficialCard(stale))
	discordClientID := "discord:123"
	_, visible = officialItemCard(turn, officialapp.Item{Type: "userMessage", ID: "user",
		ClientID: &discordClientID}, plan, uuid.Nil)
	require.False(t, visible)
	externalClientID := "mobile-client-message"
	external := officialapp.Item{Type: "userMessage", ID: "external-user",
		ClientID: &externalClientID, Content: json.RawMessage(`[
			{"type":"text","text":"从手机继续"},
			{"type":"localImage","path":"/workspace/image.png"},
			{"type":"mention","name":"README.md","path":"/workspace/README.md"}
		]`)}
	projections := officialItemProjections(turn, external, nil, uuid.Nil)
	require.Len(t, projections, 1)
	require.Contains(t, projections[0].Card.Header, "外部客户端")
	require.Contains(t, projections[0].Card.Body, "从手机继续")
	require.Contains(t, projections[0].Card.Body, "image.png")
	require.Contains(t, projections[0].Card.Body, "README.md")
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

func TestOfficialImageGenerationAndTurnErrorProjection(t *testing.T) {
	image := officialapp.Item{Type: "imageGeneration", ID: "image-1",
		Raw: json.RawMessage(`{"type":"imageGeneration","id":"image-1",
			"status":"generating","revisedPrompt":"a diagram",
			"result":"iVBORw0KGgo="}`)}
	projection := officialImageGenerationProjection(image)
	require.Len(t, projection.Files, 1)
	require.Equal(t, "image/png", projection.Files[0].MediaType)
	require.True(t, strings.HasSuffix(projection.Files[0].Filename, ".png"))
	require.Equal(t, projection.Files[0].Filename, projection.Card.Media[0].Filename)
	require.NoError(t, validateOfficialCard(projection.Card))
	files, err := decodeMessageFiles(projection.Files)
	require.NoError(t, err)
	require.Len(t, files, 1)

	invalid := image
	invalid.Raw = json.RawMessage(`{"status":"completed","result":"not-base64"}`)
	invalidProjection := officialImageGenerationProjection(invalid)
	require.Empty(t, invalidProjection.Files)
	require.Contains(t, invalidProjection.Card.Header, "失败")

	details := "network unavailable"
	errorProjection := officialTurnErrorProjection("thread-1", officialapp.Turn{
		ID: "turn-failed", Status: "failed", Error: &officialapp.TurnError{
			Message: "request failed", CodexErrorInfo: json.RawMessage(`{"kind":"network"}`),
			AdditionalDetails: &details,
		},
	})
	require.NotNil(t, errorProjection.Card.Error)
	require.Equal(t, "request failed", errorProjection.Card.Error.Message)
	require.Contains(t, errorProjection.Card.Error.AdditionalDetails, "network")
	require.NoError(t, validateOfficialCard(errorProjection.Card))
}

func TestOfficialProjectionValidatesGeneratedMediaAndExternalInputs(t *testing.T) {
	media := []struct {
		name      string
		data      []byte
		mediaType string
		extension string
	}{
		{name: "jpeg", data: []byte{0xff, 0xd8, 0xff, 0x00},
			mediaType: "image/jpeg", extension: ".jpg"},
		{name: "gif87", data: []byte("GIF87a"),
			mediaType: "image/gif", extension: ".gif"},
		{name: "gif89", data: []byte("GIF89a"),
			mediaType: "image/gif", extension: ".gif"},
		{name: "webp", data: append([]byte("RIFF0000WEBP"), 0x00),
			mediaType: "image/webp", extension: ".webp"},
	}
	for _, test := range media {
		t.Run(test.name, func(t *testing.T) {
			file, err := generatedImageFile(base64.StdEncoding.EncodeToString(test.data))
			require.NoError(t, err)
			require.Equal(t, test.mediaType, file.MediaType)
			require.True(t, strings.HasSuffix(file.Filename, test.extension))
		})
	}
	_, err := generatedImageFile(base64.StdEncoding.EncodeToString([]byte("plain")))
	require.ErrorContains(t, err, "受支持")

	png := MessageFilePayload{Filename: "image.png", MediaType: "image/png",
		Base64: "iVBORw0KGgo="}
	_, err = decodeMessageFiles([]MessageFilePayload{png, png})
	require.ErrorContains(t, err, "重复")
	invalidPath := png
	invalidPath.Filename = "nested/image.png"
	_, err = decodeMessageFiles([]MessageFilePayload{invalidPath})
	require.ErrorContains(t, err, "文件名")
	invalidBase64 := png
	invalidBase64.Base64 = "%%"
	_, err = decodeMessageFiles([]MessageFilePayload{invalidBase64})
	require.ErrorContains(t, err, "Base64")
	wrongType := png
	wrongType.MediaType = "image/jpeg"
	_, err = decodeMessageFiles([]MessageFilePayload{wrongType})
	require.ErrorContains(t, err, "类型")
	_, err = decodeMessageFiles(make([]MessageFilePayload, DefaultMaxAttachments+1))
	require.ErrorContains(t, err, "不能超过")

	clientID := "desktop:external"
	inputs := officialapp.Item{Type: "userMessage", ID: "external-inputs", ClientID: &clientID,
		Content: json.RawMessage(`[
			{"type":"image","name":"remote.png"},
			{"type":"audio","path":"/workspace/audio.wav"},
			{"type":"localAudio","url":"https://example.invalid/audio"},
			{"type":"skill","name":"review"}
		]`)}
	projection := officialUserMessageProjections(inputs)
	require.Len(t, projection, 1)
	require.Contains(t, projection[0].Card.Body, "remote.png")
	require.Contains(t, projection[0].Card.Body, "audio.wav")
	require.Contains(t, projection[0].Card.Body, "example.invalid")
	require.Contains(t, projection[0].Card.Body, "review")

	invalidInputs := inputs
	invalidInputs.Content = json.RawMessage(`[{`)
	require.Contains(t, officialUserMessageProjections(invalidInputs)[0].Card.Body, "无法解析")
	longInputs := inputs
	longInputs.Content = json.RawMessage(`[{"type":"text","text":"` +
		strings.Repeat("x", desktopInputPageRunes+1) + `"}]`)
	pages := officialUserMessageProjections(longInputs)
	require.Len(t, pages, 2)
	require.Contains(t, pages[0].Card.Header, "1/2")

	emptyImage := officialImageGenerationProjection(officialapp.Item{Type: "imageGeneration",
		ID: "empty", Raw: json.RawMessage(`{"status":"generating","result":""}`)})
	require.Empty(t, emptyImage.Files)
	malformedImage := officialImageGenerationProjection(officialapp.Item{Type: "imageGeneration",
		ID: "malformed", Raw: json.RawMessage(`{`)})
	require.Contains(t, malformedImage.Card.Header, "失败")
}

func validateOfficialCard(card ComponentCardPayload) error {
	_, err := discordCardComponents(card)
	return err
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
