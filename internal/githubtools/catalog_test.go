package githubtools

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestCatalogProducesNamespacedDynamicTools(t *testing.T) {
	catalog, err := NewCatalog([]string{"issue_read", "add_issue_comment"})
	require.NoError(t, err)
	spec, err := catalog.DynamicToolSpec()
	require.NoError(t, err)
	require.Equal(t, "namespace", spec.Type)
	require.Equal(t, "github", spec.Name)
	require.Len(t, spec.Tools, 2)
	for _, tool := range spec.Tools {
		require.Equal(t, "function", tool.Type)
		require.NotEmpty(t, tool.InputSchema)
		require.NotEmpty(t, tool.Description)
	}
}

func TestCatalogRejectsUnknownTool(t *testing.T) {
	_, err := NewCatalog([]string{"does_not_exist"})
	require.Error(t, err)
}

func TestCatalogFiltersToolsAndPreservesOfficialAnnotations(t *testing.T) {
	catalog, err := NewCatalog(RegisteredTools)
	require.NoError(t, err)
	for _, name := range RegisteredTools {
		_, err = catalog.DynamicToolSpecFor([]string{name})
		require.NoError(t, err, "注册工具 %s 必须存在于官方工具目录", name)
	}
	spec, err := catalog.DynamicToolSpecFor([]string{"issue_read", "merge_pull_request"})
	require.NoError(t, err)
	require.Len(t, spec.Tools, 2)
	require.Equal(t, "issue_read", spec.Tools[0].Name)
	require.Equal(t, "merge_pull_request", spec.Tools[1].Name)
	readOnly, ok := catalog.IsReadOnly("issue_read")
	require.True(t, ok)
	require.True(t, readOnly)
	readOnly, ok = catalog.IsReadOnly("add_issue_comment")
	require.True(t, ok)
	require.False(t, readOnly)
	_, ok = catalog.IsReadOnly("unknown")
	require.False(t, ok)
	_, err = catalog.DynamicToolSpecFor([]string{"unknown"})
	require.Error(t, err)
}

func TestConvertOfficialToolContent(t *testing.T) {
	items, err := convertContent([]mcp.Content{
		&mcp.TextContent{Text: "ok"},
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "repo://file", MIMEType: "text/plain", Text: "file contents"}},
		&mcp.ResourceLink{URI: "https://example.com/file", Name: "file", MIMEType: "text/plain"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", items[0].Text)
	require.Equal(t, "data:image/png;base64,AQID", items[1].ImageURL)
	require.Equal(t, "file contents", items[2].Text)
	require.Contains(t, items[3].Text, "https://example.com/file")
	_, err = convertContent([]mcp.Content{&mcp.AudioContent{MIMEType: "audio/wav", Data: []byte{1}}})
	require.Error(t, err)
}
