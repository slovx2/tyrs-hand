package discordintegration

import (
	"encoding/json"
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
	card = interactiveCard(request)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Discord")
}

func TestInteractiveSecretCardOnlyAllowsDesktop(t *testing.T) {
	request := InteractiveProjection{ID: uuid.New(), Status: "pending",
		Draft: map[string]json.RawMessage{}, Questions: []InteractiveQuestion{{
			ID: "secret", Header: "Secret", Question: "Token?", IsSecret: true,
		}}}
	card := interactiveCard(request)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Codex Desktop")
	require.Contains(t, card.Footer, "Secret")
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
