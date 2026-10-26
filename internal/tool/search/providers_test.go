package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient() *http.Client { return &http.Client{} }

func postEcho(t *testing.T, response string, status int) (*httptest.Server, *[]byte) {
	t.Helper()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server, &received
}

func TestTavilyProviderMapsAnswerAndResults(t *testing.T) {
	server, _ := postEcho(t, `{"answer": "synthesised", "results": [
		{"title": "T1", "url": "https://a.example/1", "content": "c1", "score": 0.9, "published_date": "2026-01-02"},
		{"title": "no-url", "url": ""}
	]}`, http.StatusOK)
	t.Setenv("TEST_TAVILY_KEY", "k")
	provider := &tavilyProvider{
		config: ProviderConfig{Name: "tavily", Type: ProviderTypeTavily, APIKeyEnv: "TEST_TAVILY_KEY", Endpoint: server.URL},
		client: newTestClient(),
	}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.ProviderUsed != "tavily" || response.Answer != "synthesised" || len(response.Results) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Results[0].Title != "T1" || response.Results[0].Snippet != "c1" || response.Results[0].PublishedAt != "2026-01-02" {
		t.Fatalf("unexpected result mapping: %#v", response.Results[0])
	}
	if response.Results[0].Score == nil || *response.Results[0].Score != 0.9 {
		t.Fatalf("score must be carried through: %#v", response.Results[0].Score)
	}
}

func TestTavilyProviderRequiresCredential(t *testing.T) {
	provider := &tavilyProvider{config: ProviderConfig{Name: "tavily", Type: ProviderTypeTavily, APIKeyEnv: "TEST_MISSING_TAVILY_KEY"}, client: newTestClient()}
	if _, err := provider.Search(context.Background(), SearchCall{Query: "q"}); err == nil {
		t.Fatal("missing key must fail with SEARCH_CREDENTIAL_REQUIRED")
	}
}

func TestDoubaoProviderParsesPascalCaseContract(t *testing.T) {
	server, received := postEcho(t, `{"Result": {"ErrorCode": 0, "TotalDocCount": 12, "Documents": [
		{"Title": "豆包结果", "Url": "https://d.example/1",
		 "Snippet": [{"Type": "text", "Text": "第一段"}, {"Type": "img", "Text": "ignore"}, {"Type": "text", "Text": "第二段"}],
		 "DocumentInfo": {"PublishTime": "2026-02-03"}, "HostInfo": {"Hostname": "d.example"}}
	]}}`, http.StatusOK)
	t.Setenv("TEST_DOUBAO_KEY", "k")
	provider := &doubaoProvider{
		config: ProviderConfig{Name: "doubao", Type: ProviderTypeDoubao, APIKeyEnv: "TEST_DOUBAO_KEY", Endpoint: server.URL},
		client: newTestClient(),
	}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q", MaxResults: 99})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("expected one result, got %#v", response.Results)
	}
	if response.Results[0].Snippet != "第一段\n第二段" || response.Results[0].PublishedAt != "2026-02-03" {
		t.Fatalf("unexpected snippet/publish mapping: %#v", response.Results[0])
	}
	var payload map[string]any
	_ = json.Unmarshal(*received, &payload)
	if payload["SearchType"] != "web" || payload["DocCount"] != float64(20) {
		t.Fatalf("doubao payload must clamp DocCount to 20: %#v", payload)
	}
}

func TestDoubaoProviderSurfacesErrorCode(t *testing.T) {
	server, _ := postEcho(t, `{"Result": {"ErrorCode": "500", "ErrorMsg": "quota"}}`, http.StatusOK)
	t.Setenv("TEST_DOUBAO_KEY", "k")
	provider := &doubaoProvider{config: ProviderConfig{Name: "doubao", APIKeyEnv: "TEST_DOUBAO_KEY", Endpoint: server.URL}, client: newTestClient()}
	if _, err := provider.Search(context.Background(), SearchCall{Query: "q"}); err == nil {
		t.Fatal("doubao ErrorCode must surface as provider failure")
	}
}

func TestDeepSeekProviderMapsSummaryAndDate(t *testing.T) {
	server, _ := postEcho(t, `{"results": [{"title": "T", "url": "https://s.example/1", "summary": "sum", "snippet": "", "date": "2026-03-04", "site_name": "S", "rerank_score": 0.5}]}`, http.StatusOK)
	t.Setenv("TEST_DEEPSEEK_KEY", "k")
	provider := &deepseekProvider{config: ProviderConfig{Name: "deepseek", APIKeyEnv: "TEST_DEEPSEEK_KEY", Endpoint: server.URL}, client: newTestClient()}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q", MaxResults: 999})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Snippet != "sum" || response.Results[0].PublishedAt != "2026-03-04" {
		t.Fatalf("unexpected deepseek mapping: %#v", response.Results)
	}
}

func TestExaProviderJoinsHighlights(t *testing.T) {
	server, _ := postEcho(t, `{"results": [{"title": "T", "url": "https://e.example/1", "highlights": ["h1", "h2"], "text": "fallback", "publishedDate": "2026-04-05"}]}`, http.StatusOK)
	t.Setenv("TEST_EXA_KEY", "k")
	provider := &exaProvider{config: ProviderConfig{Name: "exa", APIKeyEnv: "TEST_EXA_KEY", Endpoint: server.URL}, client: newTestClient()}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.Results[0].Snippet != "h1 h2" {
		t.Fatalf("highlights must be joined: %#v", response.Results[0])
	}
}

func TestKimiProviderReturnsAnswerOnly(t *testing.T) {
	server, received := postEcho(t, `{"choices": [{"message": {"content": "合成答案"}}]}`, http.StatusOK)
	t.Setenv("TEST_MOONSHOT_KEY", "k")
	provider := &kimiProvider{config: ProviderConfig{Name: "kimi", APIKeyEnv: "TEST_MOONSHOT_KEY", Endpoint: server.URL}, client: newTestClient()}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.Answer != "合成答案" || len(response.Results) != 0 {
		t.Fatalf("kimi must map to answer-only response: %#v", response)
	}
	var payload struct {
		Model    string           `json:"model"`
		Tools    []map[string]any `json:"tools"`
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(*received, &payload)
	if payload.Model != "moonshot-v1-128k" || len(payload.Tools) != 1 || payload.Tools[0]["type"] != "web_search" {
		t.Fatalf("kimi payload must keep the web_search tool contract: %#v", payload)
	}
}

func TestSearchFreeProviderFlattensResults(t *testing.T) {
	server, _ := postEcho(t, `{"results": [{"title": "T", "url": "https://f.example/1", "snippet": "s"}, {"url": ""}]}`, http.StatusOK)
	provider := &searchFreeProvider{config: ProviderConfig{Name: "searchfree", Type: ProviderTypeSearchFree, Endpoint: server.URL}, client: newTestClient()}
	response, err := provider.Search(context.Background(), SearchCall{Query: "q"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Title != "T" {
		t.Fatalf("unexpected searchfree mapping: %#v", response.Results)
	}
}

func TestHTTPProviderRendersTemplateAndMapsResponse(t *testing.T) {
	server, received := postEcho(t, `{"data": {"list": [
		{"Title": "政务结果", "Url": "https://gov.cn/1", "Abstract": "摘要", "Time": "2026-05-06", "Rank": "7"}
	]}}`, http.StatusOK)
	t.Setenv("TEST_GOV_KEY", "gov-secret")
	provider := &httpProvider{
		config: ProviderConfig{
			Name: "gov", Type: ProviderTypeHTTP, Endpoint: server.URL,
			Headers:         map[string]string{"X-Api-Key": "${TEST_GOV_KEY}"},
			RequestTemplate: map[string]any{"q": "{{query}}", "n": "{{max_results}}", "fixed": "pre-{{query}}-post"},
			ResponseMapping: &ResponseMapping{ResultsPath: "data.list", Title: "Title", URL: "Url", Snippet: "Abstract", PublishedAt: "Time", Score: "Rank"},
		},
		client: newTestClient(),
	}
	response, err := provider.Search(context.Background(), SearchCall{Query: "政务 query", MaxResults: 7})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("expected one mapped result: %#v", response.Results)
	}
	item := response.Results[0]
	if item.Title != "政务结果" || item.URL != "https://gov.cn/1" || item.Snippet != "摘要" || item.PublishedAt != "2026-05-06" {
		t.Fatalf("unexpected generic mapping: %#v", item)
	}
	if item.Score == nil || *item.Score != 7 {
		t.Fatalf("string-typed score must coerce: %#v", item.Score)
	}
	var payload map[string]any
	_ = json.Unmarshal(*received, &payload)
	if payload["q"] != "政务 query" || payload["n"] != float64(7) || payload["fixed"] != "pre-政务 query-post" {
		t.Fatalf("template placeholders rendered wrongly: %#v", payload)
	}
}

func TestHTTPProviderRejectsMissingHeaderEnv(t *testing.T) {
	server, _ := postEcho(t, `{"data": {"list": []}}`, http.StatusOK)
	provider := &httpProvider{
		config: ProviderConfig{
			Name: "gov", Type: ProviderTypeHTTP, Endpoint: server.URL,
			Headers:         map[string]string{"X-Api-Key": "${TEST_MISSING_GOV_KEY}"},
			RequestTemplate: map[string]any{"q": "{{query}}"},
			ResponseMapping: &ResponseMapping{ResultsPath: "data.list", Title: "T", URL: "U"},
		},
		client: newTestClient(),
	}
	if _, err := provider.Search(context.Background(), SearchCall{Query: "q"}); err == nil {
		t.Fatal("missing header env must fail with SEARCH_CREDENTIAL_REQUIRED")
	}
}
