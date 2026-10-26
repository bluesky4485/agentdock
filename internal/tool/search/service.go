package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

const defaultMaxResults = 5

// Service 是 web_search / web_fetch 的 capability 服务。配置在构造时加载，
// 变更需重启 Core（与 AgentDock 其余配置一致）；key 在调用时解析。
type Service struct {
	home            string
	client          *http.Client
	providers       map[string]Provider
	order           []string
	defaultProvider string
	policy          *Policy
	audit           *auditLogger
	fetcher         *fetcher
}

func New(home string) (*Service, error) {
	file, err := LoadProviders(home)
	if err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(home)
	if err != nil {
		return nil, err
	}
	service := &Service{
		home:      home,
		client:    &http.Client{Timeout: searchHTTPTimeout},
		providers: make(map[string]Provider, len(file.Providers)),
		policy:    policy,
		audit:     newAuditLogger(home),
	}
	for _, cfg := range file.Providers {
		provider, err := newProvider(cfg, service.client)
		if err != nil {
			return nil, fmt.Errorf("search provider %q: %w", cfg.Name, err)
		}
		service.providers[cfg.Name] = provider
		service.order = append(service.order, cfg.Name)
	}
	service.defaultProvider = strings.TrimSpace(file.DefaultProvider)
	service.fetcher = newFetcher(policy)
	return service, nil
}

// Close 目前无持久句柄（审计按写打开）；保留以纳入 Runtime 生命周期。
func (s *Service) Close() error { return nil }

func (s *Service) Search(ctx context.Context, request SearchRequest) (toolcore.Result, error) {
	startedAt := time.Now()
	source, traceID := auditContext(ctx)
	entry := AuditEntry{Tool: ToolWebSearch, Query: request.Query, Source: source, TraceID: traceID}
	fail := func(err error) (toolcore.Result, error) {
		entry.DurationMS = time.Since(startedAt).Milliseconds()
		entry.Outcome, entry.ErrorCode = "error", auditCode(err)
		s.audit.Log(entry)
		return nil, err
	}

	query := strings.TrimSpace(request.Query)
	if query == "" {
		return fail(toolcore.NewErrorDetails("INVALID_ARGUMENT", "query must not be empty", "validation", map[string]any{"tool": ToolWebSearch}))
	}
	entry.Query = query

	if keyword, blocked := s.policy.KeywordBlocked(query); blocked {
		entry.DurationMS = time.Since(startedAt).Milliseconds()
		entry.Outcome, entry.BlockedReason = "blocked", "blocked keyword: "+keyword
		s.audit.Log(entry)
		return nil, toolcore.NewErrorDetails(
			"SEARCH_POLICY_BLOCKED",
			"query was rejected by this node's search policy (blocked keyword)",
			"search",
			map[string]any{"tool": ToolWebSearch},
		)
	}

	provider, err := s.pickProvider(request.Provider)
	if err != nil {
		return fail(err)
	}
	entry.Provider = provider.Name()

	maxResults := defaultMaxResults
	if request.MaxResults != nil {
		maxResults = clampInt(*request.MaxResults, 1, 50)
	}
	response, err := provider.Search(ctx, SearchCall{Query: query, MaxResults: maxResults})
	if err != nil {
		return fail(err)
	}

	// 域名策略同时约束结果集：blocked 剔除，allowlist 非空时只保留白名单内结果。
	results := make([]SearchResult, 0, len(response.Results))
	for _, item := range response.Results {
		if _, allowed := s.policy.DomainAllowed(hostOf(item.URL)); allowed {
			results = append(results, item)
		}
	}
	response.Results = results

	entry.ResultCount = len(results)
	entry.DurationMS = time.Since(startedAt).Milliseconds()
	entry.Outcome = "ok"
	s.audit.Log(entry)
	return resultOf(response)
}

func (s *Service) pickProvider(name string) (Provider, error) {
	if strings.TrimSpace(name) != "" {
		provider, ok := s.providers[strings.TrimSpace(name)]
		if !ok {
			return nil, toolcore.NewErrorDetails(
				"SEARCH_PROVIDER_NOT_FOUND",
				fmt.Sprintf("search provider %q is not configured on this node", name),
				"search",
				map[string]any{"provider": name, "available": s.order},
			)
		}
		return provider, nil
	}
	if s.defaultProvider != "" {
		if provider, ok := s.providers[s.defaultProvider]; ok {
			return provider, nil
		}
	}
	if len(s.order) > 0 {
		return s.providers[s.order[0]], nil
	}
	return nil, toolcore.NewErrorDetails("SEARCH_NOT_CONFIGURED", "no search provider is configured on this node", "search", map[string]any{"available": s.order})
}

func (s *Service) Fetch(ctx context.Context, request FetchRequest) (toolcore.Result, error) {
	startedAt := time.Now()
	source, traceID := auditContext(ctx)
	entry := AuditEntry{Tool: ToolWebFetch, URL: request.URL, Source: source, TraceID: traceID}
	fail := func(err error) (toolcore.Result, error) {
		entry.DurationMS = time.Since(startedAt).Milliseconds()
		entry.Outcome, entry.ErrorCode = "error", auditCode(err)
		s.audit.Log(entry)
		return nil, err
	}

	if s.policy.WebFetch == nil || !s.policy.WebFetch.Enabled {
		return fail(toolcore.NewError("FETCH_DISABLED", "web_fetch is not enabled by this node's policy", "search"))
	}
	rawURL := strings.TrimSpace(request.URL)
	if rawURL == "" {
		return fail(toolcore.NewErrorDetails("INVALID_ARGUMENT", "url must not be empty", "validation", map[string]any{"tool": ToolWebFetch}))
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fail(toolcore.NewErrorDetails("FETCH_URL_REJECTED", "url must be an absolute http(s) URL", "search", map[string]any{"tool": ToolWebFetch}))
	}
	entry.URL = parsed.String()
	if reason, allowed := s.policy.DomainAllowed(parsed.Hostname()); !allowed {
		entry.DurationMS = time.Since(startedAt).Milliseconds()
		entry.Outcome, entry.BlockedReason = "blocked", reason
		s.audit.Log(entry)
		return nil, toolcore.NewErrorDetails(
			"FETCH_POLICY_BLOCKED",
			"url host was rejected by this node's search policy: "+reason,
			"search",
			map[string]any{"tool": ToolWebFetch},
		)
	}

	response, err := s.fetcher.Fetch(ctx, parsed)
	if err != nil {
		return fail(err)
	}
	entry.DurationMS = time.Since(startedAt).Milliseconds()
	entry.Outcome = "ok"
	s.audit.Log(entry)
	return resultOf(response)
}

// resultOf 走一次 JSON 往返，让结构体的 omitempty 语义与公开 output schema 一致。
func resultOf(value any) (toolcore.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "encode tool result", "search", nil, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "decode tool result", "search", nil, err)
	}
	return result, nil
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return normalizeHost(parsed.Host)
}
