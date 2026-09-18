package worker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyCatalogRevisionOnlyUploadsWhenChanged(t *testing.T) {
	runner := &Runner{}
	catalog := json.RawMessage(`{"data":[{"id":"a"}]}`)

	values := map[string]any{"modelCatalog": catalog}
	revision, included := runner.applyCatalogRevision(values)
	require.True(t, included)
	require.NotEmpty(t, revision)
	require.Contains(t, values, "modelCatalog")

	runner.catalogRevision = revision
	runner.catalogRevisionAt = time.Now()
	values = map[string]any{"modelCatalog": catalog}
	nextRevision, included := runner.applyCatalogRevision(values)
	require.False(t, included)
	require.Equal(t, revision, nextRevision)
	require.NotContains(t, values, "modelCatalog")
}

func TestApplyCatalogRevisionUploadsAfterRefreshInterval(t *testing.T) {
	runner := &Runner{catalogRevision: "stale",
		catalogRevisionAt: time.Now().Add(-2 * catalogHeartbeatRefresh)}
	values := map[string]any{"modelCatalog": json.RawMessage(`{"data":[]}`)}

	revision, included := runner.applyCatalogRevision(values)
	require.True(t, included)
	require.NotEqual(t, "stale", revision)
	require.Contains(t, values, "modelCatalog")
}

func TestApplyCatalogRevisionClearsRemovedCatalog(t *testing.T) {
	runner := &Runner{catalogRevision: "previous", catalogRevisionAt: time.Now()}
	values := map[string]any{}

	revision, included := runner.applyCatalogRevision(values)
	require.True(t, included)
	require.Empty(t, revision)
	require.Contains(t, values, "modelCatalog")
	require.Nil(t, values["modelCatalog"])

	runner.catalogRevision = revision
	values = map[string]any{}
	_, included = runner.applyCatalogRevision(values)
	require.False(t, included)
	require.NotContains(t, values, "modelCatalog")
}
