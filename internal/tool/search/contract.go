package search

import (
	toolcontract "github.com/uvwt/agentdock/internal/tool/contract"
)

const (
	ToolWebSearch = "web_search"
	ToolWebFetch  = "web_fetch"
)

// InputSchema / OutputSchema 是 web_search 与 web_fetch 的公开契约。
// web_search 的 provider 列表刻意不进 schema enum：新增 provider 只改节点配置，
// 不改工具契约；未知 provider 在运行时报错并携带可用列表。
func InputSchema(name string) map[string]any {
	switch name {
	case ToolWebSearch:
		return toolcontract.InputObject(map[string]any{
			"query":       toolcontract.String("Search query text. Sent verbatim to the configured provider over the node's internet egress — keep it free of internal or confidential data."),
			"provider":    toolcontract.String("Optional provider name as configured in providers.json. Omit to use the default provider; an unknown name returns an error listing the available providers."),
			"max_results": toolcontract.BoundedInteger("Maximum number of results to return. Defaults to 5; each provider applies its own tighter cap.", 1, 50),
		}, "query")
	case ToolWebFetch:
		return toolcontract.InputObject(map[string]any{
			"url": toolcontract.String("Absolute http(s) URL of the page to fetch. Cross-host redirects are followed, but every hop is re-checked against the node's domain policy; loopback and private network hosts are rejected."),
			"engine": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "http", "browser"},
				"description": "Rendering engine. auto (default) fetches over HTTP first and re-renders in a local headless Chromium when the HTML text is suspiciously thin (client-rendered pages, bot-block pages); http never renders; browser always renders. browser requires a Chromium-family browser on this node and falls back to the HTTP result on failure.",
			},
		}, "url")
	default:
		return nil
	}
}

func OutputSchema(name string) map[string]any {
	switch name {
	case ToolWebSearch:
		return toolcontract.OutputObject(map[string]any{
			"provider_used": toolcontract.String("Provider that actually served the search."),
			"answer":        toolcontract.String("Optional synthesised answer when the provider offers one (tavily, kimi); empty for list-style providers."),
			"results": map[string]any{
				"type":        "array",
				"description": "Normalised search results. Empty for answer-only providers.",
				"items": map[string]any{
					"type":        "object",
					"description": "One search hit, normalised across providers.",
					"properties": map[string]any{
						"title":        toolcontract.String("Result title."),
						"url":          toolcontract.String("Result URL."),
						"snippet":      toolcontract.String("Query-relevant excerpt."),
						"published_at": toolcontract.String("Publication date when the provider supplies one."),
						"score":        map[string]any{"type": "number", "description": "Provider relevance score when available."},
					},
					"additionalProperties": true,
				},
			},
		})
	case ToolWebFetch:
		return toolcontract.OutputObject(map[string]any{
			"final_url":     toolcontract.String("URL of the final response after redirects."),
			"status":        toolcontract.Integer("HTTP status code of the final response."),
			"content_type":  toolcontract.String("Content-Type of the final response."),
			"text":          toolcontract.String("Extracted text content; HTML is reduced to visible text."),
			"truncated":     toolcontract.Boolean("Whether the content exceeded the size limit and was cut short."),
			"render_engine": toolcontract.String("Engine that produced the returned text: http or browser (browser is used by the auto engine when the HTTP text was too thin, or when engine=browser)."),
		})
	default:
		return nil
	}
}
