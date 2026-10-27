package app

import (
	"context"

	toolsearch "github.com/uvwt/agentdock/internal/tool/search"
)

func searchToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name:         toolsearch.ToolWebSearch,
			Contract:     searchToolContract,
			Title:        "Search the web",
			Description:  "Search the public web with the providers configured on this node (providers.json). Omit provider to use the default; an unknown provider name returns an error listing the available ones. The query leaves the node's internet egress — keep it free of internal or confidential data.",
			Annotations:  readOnlyToolAnnotations(true),
			Availability: requiresWebSearch,
			Handler: typedToolHandler(toolsearch.ToolWebSearch, func(ctx context.Context, r *Runtime, request toolsearch.SearchRequest) (Result, error) {
				return r.search.Search(ctx, request)
			}),
		},
		{
			Name:         toolsearch.ToolWebFetch,
			Contract:     searchToolContract,
			Title:        "Fetch a web page",
			Description:  "Fetch one http(s) URL from the public web and return the extracted text. Cross-host redirects are followed but every hop is re-checked against the node's domain policy; loopback/private hosts are rejected. engine=auto (default) re-renders thin HTML pages in a local headless Chromium when available; engine=http never renders; engine=browser always renders. Read-only, capped at 1MB/15s. Use it to read pages found via web_search.",
			Annotations:  readOnlyToolAnnotations(true),
			Availability: requiresWebFetch,
			Handler: typedToolHandler(toolsearch.ToolWebFetch, func(ctx context.Context, r *Runtime, request toolsearch.FetchRequest) (Result, error) {
				return r.search.Fetch(ctx, request)
			}),
		},
	}
}
