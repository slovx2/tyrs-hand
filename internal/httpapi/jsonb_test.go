package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONBSafeStripsNullAndKeepsText(t *testing.T) {
	raw := json.RawMessage(`{"snippet":"a\u0000b","ok":"web"}`)
	safe := jsonbSafe(raw)
	var value map[string]any
	require.NoError(t, json.Unmarshal(safe, &value))
	require.Equal(t, "ab", value["snippet"])
	require.Equal(t, "web", value["ok"])
	require.NotContains(t, string(safe), `\u0000`)
}

func TestJSONBSafeSanitizesObjectKeys(t *testing.T) {
	raw := json.RawMessage(`{"a\u0000b":1}`)
	safe := jsonbSafe(raw)
	var value map[string]any
	require.NoError(t, json.Unmarshal(safe, &value))
	_, hasBroken := value["a\x00b"]
	require.False(t, hasBroken)
	require.EqualValues(t, 1, value["ab"])
}

func TestJSONBSafePreservesNumbers(t *testing.T) {
	raw := json.RawMessage(`{"n":12345678901234567890,"nested":[{"x":1.5}]}`)
	safe := jsonbSafe(raw)
	require.Contains(t, string(safe), "12345678901234567890")
}

func TestJSONBSafeInvalidJSONUsesEnvelope(t *testing.T) {
	safe := jsonbSafe(json.RawMessage(`{not json`))
	var value map[string]string
	require.NoError(t, json.Unmarshal(safe, &value))
	require.Equal(t, "base64", value["encoding"])
	require.NotEmpty(t, value["data"])
}

func TestJSONBSafeEmptyBecomesObject(t *testing.T) {
	require.JSONEq(t, `{}`, string(jsonbSafe(nil)))
	require.JSONEq(t, `{}`, string(jsonbSafe(json.RawMessage("null"))))
	require.JSONEq(t, `{}`, string(jsonbSafe(json.RawMessage("  "))))
}

func TestJSONBSafeNestedArray(t *testing.T) {
	raw := json.RawMessage(`{"results":[{"snippet":"ok\u0000"}]}`)
	safe := jsonbSafe(raw)
	require.False(t, strings.Contains(string(safe), `\u0000`))
	require.Contains(t, string(safe), `"ok"`)
}
