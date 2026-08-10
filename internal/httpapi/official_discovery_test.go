package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfficialThreadStarterCardDoesNotRepeatThreadPreview(t *testing.T) {
	const preview = "TYRS-E2E-unique-user-input"

	card := officialThreadStarterCard()

	require.Equal(t, "🖥️ Desktop · Desktop", card.Header)
	require.Equal(t, "已从 Codex Desktop 连接这个官方 Thread。", card.Body)
	require.NotContains(t, card.Body, preview)
}
