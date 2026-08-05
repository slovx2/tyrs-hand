package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidInteractiveAnswerRequiresCodexAnswerObjects(t *testing.T) {
	require.True(t, validInteractiveAnswer(json.RawMessage(
		`{"answers":{"choice":{"answers":["继续"]},"note":{"answers":["补充"]}}}`)))
	require.True(t, validInteractiveAnswer(json.RawMessage(`{"answers":{}}`)))
	require.False(t, validInteractiveAnswer(json.RawMessage(
		`{"answers":{"choice":"继续"}}`)))
}
