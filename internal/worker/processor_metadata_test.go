package worker

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/stretchr/testify/require"
)

func TestHeartbeatMetadataIncludesHostAndModelCatalog(t *testing.T) {
	workspaceID := uuid.New()
	processor := &Processor{}
	processor.UseHostRuntime(&hostworker.Runtime{}, workspaceID,
		json.RawMessage(`{"data":[{"id":"gpt-test"}]}`))

	metadata := processor.HeartbeatMetadata()
	require.Contains(t, metadata, "host")
	catalog, ok := metadata["modelCatalog"].(json.RawMessage)
	require.True(t, ok)
	require.NotContains(t, metadata, "modelCatalogs")
	require.JSONEq(t, `{"data":[{"id":"gpt-test"}]}`, string(catalog))
}
