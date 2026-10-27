package search

// SearchRequest 是 web_search 进入 capability 的稳定输入契约。
type SearchRequest struct {
	Query      string `json:"query"`
	Provider   string `json:"provider,omitempty"`
	MaxResults *int   `json:"max_results,omitempty"`
}

// SearchResult 是各 provider 响应归一化后的单条结果。
type SearchResult struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Snippet     string   `json:"snippet,omitempty"`
	PublishedAt string   `json:"published_at,omitempty"`
	Score       *float64 `json:"score,omitempty"`
}

// SearchResponse 是 provider 层的统一响应。Answer-only provider（kimi）只填 Answer。
type SearchResponse struct {
	ProviderUsed string         `json:"provider_used"`
	Answer       string         `json:"answer,omitempty"`
	Results      []SearchResult `json:"results"`
}

// FetchRequest 是 web_fetch 的稳定输入契约。Engine 选择渲染方式：
// auto（默认）HTTP 先行、内容过薄时浏览器渲染一次；http/browser 显式指定。
type FetchRequest struct {
	URL    string `json:"url"`
	Engine string `json:"engine,omitempty"`
}

// FetchResponse 是 web_fetch 的统一响应。RenderEngine 说明最终文本来自哪次渲染。
type FetchResponse struct {
	FinalURL     string `json:"final_url"`
	Status       int    `json:"status"`
	ContentType  string `json:"content_type"`
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated"`
	RenderEngine string `json:"render_engine,omitempty"`
}
