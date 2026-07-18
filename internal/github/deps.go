package github

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	ghmcp "github.com/github/github-mcp-server/pkg/github"
	"github.com/github/github-mcp-server/pkg/lockdown"
	"github.com/github/github-mcp-server/pkg/observability"
	"github.com/github/github-mcp-server/pkg/observability/metrics"
	"github.com/github/github-mcp-server/pkg/raw"
	"github.com/github/github-mcp-server/pkg/translations"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

func (a *AppClient) ToolDependencies(ctx context.Context, installationID int64) (ghmcp.ToolDependencies, error) {
	token, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	rest, err := a.RESTClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	httpClient.Timeout = 30 * time.Second
	graphQLURL := a.apiURL.ResolveReference(&url.URL{Path: "graphql"})
	gql := githubv4.NewEnterpriseClient(graphQLURL.String(), httpClient)
	rawURL := a.apiURL.ResolveReference(&url.URL{Path: "raw/"})
	rawClient, err := raw.NewClient(rest, rawURL)
	if err != nil {
		return nil, err
	}
	repoAccess := lockdown.NewRepoAccessCache(gql, rest)
	obs, err := observability.NewExporters(slog.New(slog.DiscardHandler), metrics.NewNoopMetrics())
	if err != nil {
		return nil, err
	}
	return ghmcp.NewBaseDeps(
		rest, gql, rawClient, repoAccess, translations.NullTranslationHelper,
		ghmcp.FeatureFlags{}, 20000,
		func(context.Context, string) (bool, error) { return false, nil }, obs,
	), nil
}
