package githubtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	ghmcp "github.com/github/github-mcp-server/pkg/github"
	"github.com/github/github-mcp-server/pkg/inventory"
	"github.com/github/github-mcp-server/pkg/translations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
)

var DefaultAllowedTools = []string{
	"add_issue_comment",
	"create_pull_request",
	"get_commit",
	"get_file_contents",
	"issue_read",
	"label_write",
	"list_branches",
	"list_commits",
	"pull_request_read",
	"pull_request_review_write",
}

var RegisteredTools = append(append([]string{}, DefaultAllowedTools...),
	"issue_write", "update_pull_request", "merge_pull_request")

var DangerousTools = map[string]bool{
	"merge_pull_request": true,
	"delete_file":        true,
}

type Catalog struct {
	tools map[string]inventory.ServerTool
	order []string
}

func NewCatalog(allowed []string) (*Catalog, error) {
	if len(allowed) == 0 {
		allowed = DefaultAllowedTools
	}
	builder := ghmcp.NewInventory(translations.NullTranslationHelper).
		WithDeprecatedAliases(ghmcp.DeprecatedToolAliases).
		WithToolsets([]string{}).
		WithTools(allowed).
		WithFeatureChecker(func(context.Context, string) (bool, error) { return false, nil })
	inv, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("构建 GitHub 工具清单: %w", err)
	}
	items := inv.ToolsForRegistration(context.Background())
	catalog := &Catalog{tools: make(map[string]inventory.ServerTool, len(items))}
	for _, item := range items {
		catalog.tools[item.Tool.Name] = item
		catalog.order = append(catalog.order, item.Tool.Name)
	}
	sort.Strings(catalog.order)
	return catalog, nil
}

func (c *Catalog) DynamicToolSpec() (ports.DynamicToolSpec, error) {
	return c.DynamicToolSpecFor(c.order)
}

func (c *Catalog) DynamicToolSpecFor(allowed []string) (ports.DynamicToolSpec, error) {
	functions := make([]ports.DynamicToolSpec, 0, len(allowed))
	for _, name := range allowed {
		item, ok := c.tools[name]
		if !ok {
			return ports.DynamicToolSpec{}, fmt.Errorf("请求的 GitHub 工具 %s 不在已注册清单中", name)
		}
		tool := item.Tool
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ports.DynamicToolSpec{}, fmt.Errorf("序列化工具 %s Schema: %w", name, err)
		}
		functions = append(functions, ports.DynamicToolSpec{
			Type: "function", Name: tool.Name, Description: tool.Description,
			InputSchema: schema,
		})
	}
	return ports.DynamicToolSpec{
		Type: "namespace", Name: "github", Description: "Operate on the authorized GitHub work item.",
		Tools: functions,
	}, nil
}

func (c *Catalog) IsReadOnly(name string) (bool, bool) {
	tool, ok := c.tools[name]
	if !ok {
		return false, false
	}
	return tool.IsReadOnly(), true
}

func (c *Catalog) Execute(ctx context.Context, deps ghmcp.ToolDependencies, name string, arguments json.RawMessage) (codex.ToolCallResult, error) {
	tool, ok := c.tools[name]
	if !ok {
		return codex.ToolCallResult{}, fmt.Errorf("请求的 GitHub 工具 %s 未授权", name)
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: arguments}}
	result, err := tool.Handler(deps)(ghmcp.ContextWithDeps(ctx, deps), request)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if result == nil {
		return codex.TextToolResult("", true), nil
	}
	items, err := convertContent(result.Content)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if len(items) == 0 && result.StructuredContent != nil {
		data, marshalErr := json.Marshal(result.StructuredContent)
		if marshalErr != nil {
			return codex.ToolCallResult{}, marshalErr
		}
		items = append(items, codex.ToolContentItem{Type: "inputText", Text: string(data)})
	}
	return codex.ToolCallResult{ContentItems: items, Success: !result.IsError}, nil
}

func convertContent(content []mcp.Content) ([]codex.ToolContentItem, error) {
	items := make([]codex.ToolContentItem, 0, len(content))
	for _, item := range content {
		switch value := item.(type) {
		case *mcp.TextContent:
			items = append(items, codex.ToolContentItem{Type: "inputText", Text: value.Text})
		case *mcp.ImageContent:
			uri := "data:" + value.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(value.Data)
			items = append(items, codex.ToolContentItem{Type: "inputImage", ImageURL: uri})
		case *mcp.EmbeddedResource:
			if value.Resource == nil {
				return nil, errors.New("上游 GitHub 工具返回了空资源")
			}
			if value.Resource.Text != "" {
				items = append(items, codex.ToolContentItem{Type: "inputText", Text: value.Resource.Text})
				continue
			}
			if len(value.Resource.Blob) > 0 && strings.HasPrefix(value.Resource.MIMEType, "image/") {
				uri := "data:" + value.Resource.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(value.Resource.Blob)
				items = append(items, codex.ToolContentItem{Type: "inputImage", ImageURL: uri})
				continue
			}
			return nil, fmt.Errorf("上游 GitHub 工具返回了不支持的资源类型 %s", value.Resource.MIMEType)
		case *mcp.ResourceLink:
			data, err := json.Marshal(map[string]string{
				"uri": value.URI, "name": value.Name, "title": value.Title,
				"description": value.Description, "mimeType": value.MIMEType,
			})
			if err != nil {
				return nil, err
			}
			items = append(items, codex.ToolContentItem{Type: "inputText", Text: string(data)})
		default:
			return nil, errors.New("上游 GitHub 工具返回了 Codex dynamic tools 不支持的内容类型")
		}
	}
	return items, nil
}
