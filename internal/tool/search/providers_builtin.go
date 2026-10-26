package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// tavilyProvider 迁移自 octop tavily_search（langchain-tavily SDK 的 REST 等价实现）。
type tavilyProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *tavilyProvider) Name() string { return p.config.Name }

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

type tavilyResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Content       string   `json:"content"`
	Score         *float64 `json:"score"`
	PublishedDate string   `json:"published_date"`
}

func (p *tavilyProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	key, err := resolveAPIKey(p.config)
	if err != nil {
		return nil, err
	}
	endpoint := endpoint(p.config, defaultTavilyEndpoint)
	payload := map[string]any{
		"query":          call.Query,
		"max_results":    clampInt(call.MaxResults, 1, 20),
		"search_depth":   "basic",
		"include_answer": true,
	}
	data, status, err := postJSON(ctx, p.client, endpoint, map[string]string{"Authorization": "Bearer " + key}, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	var parsed tavilyResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     strings.TrimSpace(item.Content),
			PublishedAt: strings.TrimSpace(item.PublishedDate),
			Score:       item.Score,
		})
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Answer: strings.TrimSpace(parsed.Answer), Results: results}, nil
}

// doubaoProvider 迁移自 octop doubao_search（豆包搜索 Global 版，PascalCase 契约）。
type doubaoProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *doubaoProvider) Name() string { return p.config.Name }

type doubaoResponse struct {
	Result *doubaoResult `json:"Result"`
}

type doubaoResult struct {
	ErrorCode     any            `json:"ErrorCode"`
	ErrorMsg      string         `json:"ErrorMsg"`
	TotalDocCount int            `json:"TotalDocCount"`
	Documents     []doubaoRecord `json:"Documents"`
}

type doubaoRecord struct {
	Title        string        `json:"Title"`
	Url          string        `json:"Url"`
	Snippet      []doubaoPiece `json:"Snippet"`
	DocumentInfo struct {
		PublishTime string `json:"PublishTime"`
	} `json:"DocumentInfo"`
	HostInfo struct {
		Hostname       string `json:"Hostname"`
		AuthorityLevel any    `json:"AuthorityLevel"`
	} `json:"HostInfo"`
}

type doubaoPiece struct {
	Type string `json:"Type"`
	Text string `json:"Text"`
}

func (p *doubaoProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	key, err := resolveAPIKey(p.config)
	if err != nil {
		return nil, err
	}
	endpoint := endpoint(p.config, defaultDoubaoEndpoint)
	payload := map[string]any{
		"SearchType":       "web",
		"Query":            truncateRunes(call.Query, 100),
		"DocCount":         clampInt(call.MaxResults, 1, 20),
		"MaxSnippetLength": 1000,
	}
	data, status, err := postJSON(ctx, p.client, endpoint, map[string]string{"Authorization": "Bearer " + key}, payload)
	if err != nil {
		return nil, err
	}
	var parsed doubaoResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	if parsed.Result == nil {
		return nil, upstreamError(p.config.Name, "returned no Result payload")
	}
	// ErrorCode 可能是数字 0 或字符串 "0"，与 octop 侧判定保持一致。
	switch code := parsed.Result.ErrorCode.(type) {
	case nil:
	case float64:
		if code != 0 {
			return nil, upstreamError(p.config.Name, fmt.Sprintf("code %v: %s", code, parsed.Result.ErrorMsg))
		}
	case string:
		if code != "" && code != "0" {
			return nil, upstreamError(p.config.Name, fmt.Sprintf("code %s: %s", code, parsed.Result.ErrorMsg))
		}
	default:
		return nil, upstreamError(p.config.Name, fmt.Sprintf("code %v: %s", code, parsed.Result.ErrorMsg))
	}
	results := make([]SearchResult, 0, len(parsed.Result.Documents))
	for _, doc := range parsed.Result.Documents {
		if strings.TrimSpace(doc.Url) == "" {
			continue
		}
		parts := make([]string, 0, len(doc.Snippet))
		for _, piece := range doc.Snippet {
			if piece.Type == "text" && strings.TrimSpace(piece.Text) != "" {
				parts = append(parts, piece.Text)
			}
		}
		results = append(results, SearchResult{
			Title:       doc.Title,
			URL:         doc.Url,
			Snippet:     strings.Join(parts, "\n"),
			PublishedAt: strings.TrimSpace(doc.DocumentInfo.PublishTime),
		})
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Results: results}, nil
}

// deepseekProvider 迁移自 octop deepseek_search（独立搜索 API，非 LLM 联网）。
type deepseekProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *deepseekProvider) Name() string { return p.config.Name }

type deepseekResponse struct {
	Results []deepseekRecord `json:"results"`
}

type deepseekRecord struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Summary     string   `json:"summary"`
	Snippet     string   `json:"snippet"`
	Date        string   `json:"date"`
	SiteName    string   `json:"site_name"`
	RerankScore *float64 `json:"rerank_score"`
}

func (p *deepseekProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	key, err := resolveAPIKey(p.config)
	if err != nil {
		return nil, err
	}
	endpoint := endpoint(p.config, defaultDeepSeekEndpoint)
	payload := map[string]any{
		"query":        truncateRunes(call.Query, 128),
		"count":        clampInt(call.MaxResults, 1, 50),
		"need_content": false,
	}
	data, status, err := postJSON(ctx, p.client, endpoint, map[string]string{"Authorization": "Bearer " + key}, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	var parsed deepseekResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		snippet := item.Summary
		if strings.TrimSpace(snippet) == "" {
			snippet = item.Snippet
		}
		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     strings.TrimSpace(snippet),
			PublishedAt: strings.TrimSpace(item.Date),
			Score:       item.RerankScore,
		})
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Results: results}, nil
}

// exaProvider 迁移自 octop exa_search（highlight contents）。
type exaProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *exaProvider) Name() string { return p.config.Name }

type exaResponse struct {
	Results []exaRecord `json:"results"`
}

type exaRecord struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Highlights    []string `json:"highlights"`
	Text          string   `json:"text"`
	PublishedDate string   `json:"publishedDate"`
	Author        string   `json:"author"`
}

func (p *exaProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	key, err := resolveAPIKey(p.config)
	if err != nil {
		return nil, err
	}
	endpoint := endpoint(p.config, defaultExaEndpoint)
	payload := map[string]any{
		"query":      call.Query,
		"numResults": clampInt(call.MaxResults, 1, 20),
		"contents":   map[string]any{"highlights": true},
	}
	data, status, err := postJSON(ctx, p.client, endpoint, map[string]string{"x-api-key": key}, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	var parsed exaResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		snippet := strings.Join(item.Highlights, " ")
		if strings.TrimSpace(snippet) == "" {
			snippet = item.Text
		}
		results = append(results, SearchResult{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     strings.TrimSpace(snippet),
			PublishedAt: strings.TrimSpace(item.PublishedDate),
		})
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Results: results}, nil
}

// kimiProvider 迁移自 octop kimi_search：moonshot-v1 自带 web_search 工具的
// 黑盒调用，返回合成答案而非结果列表，映射到统一响应的 Answer 字段。
type kimiProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *kimiProvider) Name() string { return p.config.Name }

type kimiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (p *kimiProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	key, err := resolveAPIKey(p.config)
	if err != nil {
		return nil, err
	}
	endpoint := endpoint(p.config, defaultKimiEndpoint)
	payload := map[string]any{
		"model":       "moonshot-v1-128k",
		"messages":    []map[string]string{{"role": "user", "content": call.Query}},
		"tools":       []any{map[string]string{"type": "web_search"}},
		"temperature": 0.3,
	}
	data, status, err := postJSON(ctx, p.client, endpoint, map[string]string{"Authorization": "Bearer " + key}, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	var parsed kimiResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	if len(parsed.Choices) == 0 {
		return nil, upstreamError(p.config.Name, "returned no choices")
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Answer: strings.TrimSpace(parsed.Choices[0].Message.Content)}, nil
}

// searchFreeProvider 迁移自 octop searchfree_search：免 key 公共兜底源。
// 与 octop 的差异：这里必须显式写进 providers.json 才会出现（政务 profile 默认不开）。
type searchFreeProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *searchFreeProvider) Name() string { return p.config.Name }

func (p *searchFreeProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	endpoint := endpoint(p.config, defaultSearchFree)
	if envEndpoint := strings.TrimSpace(os.Getenv("SEARCHFREE_ENDPOINT")); envEndpoint != "" && p.config.Endpoint == "" {
		endpoint = envEndpoint
	}
	payload := map[string]any{
		"query":        call.Query,
		"max_results":  clampInt(call.MaxResults, 1, 50),
		"search_depth": "advanced",
	}
	data, status, err := postJSON(ctx, p.client, endpoint, nil, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d", status))
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, decodeJSONError(endpoint, err)
	}
	results := make([]SearchResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		snippet := item.Snippet
		if strings.TrimSpace(snippet) == "" {
			snippet = item.Content
		}
		results = append(results, SearchResult{Title: item.Title, URL: item.URL, Snippet: strings.TrimSpace(snippet)})
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Results: results}, nil
}
