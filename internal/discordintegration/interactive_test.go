package discordintegration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInteractiveCardAdvancesQuestionsAndDisablesResolvedActions(t *testing.T) {
	id := uuid.New()
	request := InteractiveProjection{ID: id, Status: "pending",
		Draft: map[string]json.RawMessage{}, Questions: []InteractiveQuestion{
			{ID: "choice", Header: "Choose", Question: "Continue?", Options: []InteractiveOption{
				{Label: "Yes", Description: "Continue."}, {Label: "No", Description: "Stop."},
			}},
			{ID: "detail", Header: "Detail", Question: "Why?"},
		}}
	card := interactiveCard(request)
	require.Len(t, card.Buttons, 3)
	parsedID, question, option, err := parseInteractiveButton(card.Buttons[0].CustomID)
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.Equal(t, 0, question)
	require.Equal(t, 0, option)

	request.Draft["choice"] = json.RawMessage(`{"answers":["Yes"]}`)
	card = interactiveCard(request)
	require.Len(t, card.Buttons, 1)
	require.Equal(t, "填写答案", card.Buttons[0].Label)
	_, question, option, err = parseInteractiveButton(card.Buttons[0].CustomID)
	require.NoError(t, err)
	require.Equal(t, 1, question)
	require.Equal(t, -1, option)

	request.Status, request.Surface = "resolved", "discord"
	request.Answer = json.RawMessage(`{"answers":{
		"choice":{"answers":["Yes"]},"detail":{"answers":["Because"]}}}`)
	card = interactiveCard(request)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Discord")
	require.Len(t, card.Sections, 2)
	require.Contains(t, card.Sections[0], "Continue?")
	require.Contains(t, card.Sections[0], "Yes")
	require.Contains(t, card.Sections[1], "Why?")
	require.Contains(t, card.Sections[1], "Because")
}

func TestInteractiveSecretCardOnlyAllowsDesktop(t *testing.T) {
	request := InteractiveProjection{ID: uuid.New(), Status: "pending",
		Draft: map[string]json.RawMessage{}, Questions: []InteractiveQuestion{{
			ID: "secret", Header: "Secret", Question: "Token?", IsSecret: true,
		}}}
	card := interactiveCard(request)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Codex Desktop")
}

func TestInteractiveResolvedCardUsesDesktopAnswerAndHidesSecret(t *testing.T) {
	request := InteractiveProjection{ID: uuid.New(), Status: "resolved", Surface: "desktop",
		Draft: map[string]json.RawMessage{}, Questions: []InteractiveQuestion{
			{ID: "detail", Header: "说明", Question: "为什么？"},
			{ID: "secret", Header: "密钥", Question: "Token？", IsSecret: true},
		}, Answer: json.RawMessage(`{"answers":{
			"detail":{"answers":["桌面答案"]},
			"secret":{"answers":["sk_should_never_be_visible_123456789"]}}}`)}

	card := interactiveCard(request)
	require.Contains(t, card.Body, "Codex Desktop")
	require.Contains(t, card.Sections[0], "桌面答案")
	require.Contains(t, card.Sections[1], "敏感回答已在 Codex Desktop 提交")
	require.NotContains(t, strings.Join(card.Sections, "\n"), "should_never")
}

func TestInteractiveResolvedCardSplitsLongSectionsWithoutDroppingContent(t *testing.T) {
	answer := strings.Repeat("长", 5000)
	encoded, err := json.Marshal(map[string]any{"answers": map[string]any{
		"detail": map[string]any{"answers": []string{answer}},
	}})
	require.NoError(t, err)
	card := interactiveCard(InteractiveProjection{Status: "resolved", Surface: "discord",
		Draft: map[string]json.RawMessage{}, Questions: []InteractiveQuestion{
			{ID: "detail", Header: "说明", Question: "内容？"},
		}, Answer: encoded})

	require.Greater(t, len(card.Sections), 1)
	for _, section := range card.Sections {
		require.LessOrEqual(t, len([]rune(section)), 4000)
	}
	require.Equal(t, 5000, strings.Count(strings.Join(card.Sections, ""), "长"))
}

func TestInteractiveModalIdentifierRoundTrip(t *testing.T) {
	id := uuid.New()
	parsed, question, err := parseInteractiveModal(interactiveModalPrefix + id.String() + ":2")
	require.NoError(t, err)
	require.Equal(t, id, parsed)
	require.Equal(t, 2, question)
	_, _, err = parseInteractiveModal("invalid")
	require.Error(t, err)
}
