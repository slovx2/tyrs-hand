package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfficialServiceTierForConversation(t *testing.T) {
	value := func(input string) *string { return &input }
	for _, test := range []struct {
		name     string
		input    *string
		expected string
	}{
		{name: "unset", input: nil, expected: ""},
		{name: "empty", input: value(""), expected: ""},
		{name: "default", input: value("default"), expected: "standard"},
		{name: "standard", input: value("standard"), expected: "standard"},
		{name: "priority", input: value("priority"), expected: "fast"},
		{name: "fast", input: value("fast"), expected: "fast"},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := officialServiceTierForConversation(test.input)
			require.NoError(t, err)
			require.Equal(t, test.expected, actual)
		})
	}
	_, err := officialServiceTierForConversation(value("unknown"))
	require.Error(t, err)
}
