package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

// httpProvider 是配置驱动的通用 REST provider：静态头鉴权 + 请求模板 +
// 响应字段映射，面向政务内网搜索网关等"静态 key + 简单 JSON"场景。
// 加这类源只改 providers.json，不改代码。
type httpProvider struct {
	config ProviderConfig
	client *http.Client
}

func (p *httpProvider) Name() string { return p.config.Name }

func (p *httpProvider) Search(ctx context.Context, call SearchCall) (*SearchResponse, error) {
	headers, err := p.resolveHeaders()
	if err != nil {
		return nil, err
	}
	payload, err := p.renderTemplate(call)
	if err != nil {
		return nil, err
	}
	data, status, err := postJSON(ctx, p.client, p.config.Endpoint, headers, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("HTTP %d: %.300s", status, string(data)))
	}
	mapping := p.config.ResponseMapping
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, decodeJSONError(p.config.Endpoint, err)
	}
	items := resolvePath(root, mapping.ResultsPath)
	list, ok := items.([]any)
	if !ok {
		return nil, upstreamError(p.config.Name, fmt.Sprintf("response path %q did not resolve to an array", mapping.ResultsPath))
	}
	results := make([]SearchResult, 0, len(list))
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		result := SearchResult{
			Title:       stringAtPath(item, mapping.Title),
			URL:         stringAtPath(item, mapping.URL),
			Snippet:     strings.TrimSpace(stringAtPath(item, mapping.Snippet)),
			PublishedAt: strings.TrimSpace(stringAtPath(item, mapping.PublishedAt)),
		}
		if score, ok := scoreAtPath(item, mapping.Score); ok {
			result.Score = &score
		}
		if strings.TrimSpace(result.URL) == "" {
			continue
		}
		results = append(results, result)
	}
	return &SearchResponse{ProviderUsed: p.config.Name, Results: results}, nil
}

// resolveHeaders 展开 header 值中的 ${ENV} 引用；引用的环境变量缺失视为配置错误，
// 避免 key 静默变成空串打到对端。
func (p *httpProvider) resolveHeaders() (map[string]string, error) {
	headers := make(map[string]string, len(p.config.Headers))
	for name, value := range p.config.Headers {
		resolved := envInterpolation.ReplaceAllStringFunc(value, func(match string) string {
			env := match[2 : len(match)-1]
			if value, ok := os.LookupEnv(env); ok {
				return value
			}
			return "\x00missing:" + env
		})
		if strings.Contains(resolved, "\x00missing:") {
			index := strings.Index(resolved, "\x00missing:")
			env := resolved[index+len("\x00missing:"):]
			env = strings.SplitN(env, "\x00", 2)[0]
			return nil, toolcore.NewErrorDetails(
				"SEARCH_CREDENTIAL_REQUIRED",
				fmt.Sprintf("search provider %q header %q references missing environment variable %s", p.config.Name, name, env),
				"search",
				map[string]any{"provider": p.config.Name, "env": env},
			)
		}
		headers[name] = resolved
	}
	return headers, nil
}

// renderTemplate 深拷贝请求模板并替换占位符。整值 "{{query}}" / "{{max_results}}"
// 替换为对应的字符串/数值；嵌在更长文本里的占位符按字符串内联替换。
func (p *httpProvider) renderTemplate(call SearchCall) (map[string]any, error) {
	if p.config.RequestTemplate == nil {
		return nil, toolcore.NewErrorDetails(
			"SEARCH_CONFIG_INVALID",
			fmt.Sprintf("search provider %q: generic http provider requires request_template", p.config.Name),
			"search",
			map[string]any{"provider": p.config.Name},
		)
	}
	rendered, err := renderValue(p.config.RequestTemplate, call)
	if err != nil {
		return nil, err
	}
	manifest, ok := rendered.(map[string]any)
	if !ok {
		return nil, upstreamError(p.config.Name, "request_template must be a JSON object")
	}
	return manifest, nil
}

func renderValue(value any, call SearchCall) (any, error) {
	switch typed := value.(type) {
	case string:
		return renderString(typed, call), nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			rendered, err := renderValue(item, call)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			rendered, err := renderValue(item, call)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered)
		}
		return out, nil
	default:
		return value, nil
	}
}

func renderString(value string, call SearchCall) any {
	switch value {
	case "{{query}}":
		return call.Query
	case "{{max_results}}":
		return call.MaxResults
	default:
		if !strings.Contains(value, "{{") {
			return value
		}
		return strings.ReplaceAll(strings.ReplaceAll(value, "{{query}}", call.Query), "{{max_results}}", fmt.Sprintf("%d", call.MaxResults))
	}
}

// resolvePath 按 "a.b.0.c" 点分路径取值；空路径返回根值。
func resolvePath(root any, path string) any {
	current := root
	for _, segment := range strings.Split(strings.TrimSpace(path), ".") {
		if segment == "" {
			continue
		}
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok {
				return nil
			}
			current = next
		case []any:
			index := 0
			if _, err := fmt.Sscanf(segment, "%d", &index); err != nil || index < 0 || index >= len(typed) {
				return nil
			}
			current = typed[index]
		default:
			return nil
		}
	}
	return current
}

func stringAtPath(item map[string]any, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	switch value := resolvePath(item, path).(type) {
	case string:
		return value
	case float64:
		return fmt.Sprintf("%g", value)
	case bool:
		return fmt.Sprintf("%t", value)
	default:
		return ""
	}
}

func scoreAtPath(item map[string]any, path string) (float64, bool) {
	if strings.TrimSpace(path) == "" {
		return 0, false
	}
	switch value := resolvePath(item, path).(type) {
	case float64:
		return value, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
