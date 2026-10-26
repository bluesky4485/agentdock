package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

// SearchCall 是 provider 适配器的统一调用参数。MaxResults 已按全局边界收敛，
// 适配器内部再按各自 provider 的上限进一步收敛。
type SearchCall struct {
	Query      string
	MaxResults int
}

// Provider 是搜索服务商的编译期适配器接口。
type Provider interface {
	Name() string
	Search(ctx context.Context, call SearchCall) (*SearchResponse, error)
}

const (
	searchHTTPTimeout   = 30 * time.Second
	maxProviderBodySize = 4 << 20
)

func newProvider(cfg ProviderConfig, client *http.Client) (Provider, error) {
	switch cfg.Type {
	case ProviderTypeTavily:
		return &tavilyProvider{config: cfg, client: client}, nil
	case ProviderTypeDoubao:
		return &doubaoProvider{config: cfg, client: client}, nil
	case ProviderTypeDeepSeek:
		return &deepseekProvider{config: cfg, client: client}, nil
	case ProviderTypeExa:
		return &exaProvider{config: cfg, client: client}, nil
	case ProviderTypeKimi:
		return &kimiProvider{config: cfg, client: client}, nil
	case ProviderTypeSearchFree:
		return &searchFreeProvider{config: cfg, client: client}, nil
	case ProviderTypeHTTP:
		return &httpProvider{config: cfg, client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported search provider type %q", cfg.Type)
	}
}

// resolveAPIKey 在调用时解析 key，而不是在构造时：key 缺失不应让节点起不来，
// 只应让该 provider 的调用报错。内联 key（GUI 配置）优先于环境变量引用。
func resolveAPIKey(cfg ProviderConfig) (string, error) {
	if strings.TrimSpace(cfg.APIKey) != "" {
		return strings.TrimSpace(cfg.APIKey), nil
	}
	if strings.TrimSpace(cfg.APIKeyEnv) == "" {
		return "", nil
	}
	key := strings.TrimSpace(os.Getenv(cfg.APIKeyEnv))
	if key == "" {
		return "", toolcore.NewErrorDetails(
			"SEARCH_CREDENTIAL_REQUIRED",
			fmt.Sprintf("search provider %q requires an API key: configure it in the node search settings or set %s", cfg.Name, cfg.APIKeyEnv),
			"search",
			map[string]any{"provider": cfg.Name, "env": cfg.APIKeyEnv},
		)
	}
	return key, nil
}

func endpoint(cfg ProviderConfig, fallback string) string {
	if strings.TrimSpace(cfg.Endpoint) != "" {
		return strings.TrimSpace(cfg.Endpoint)
	}
	return fallback
}

// postJSON 是所有 REST provider 的公共出站调用：JSON 编解码 + 超时 + 响应体限量。
func postJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, payload any) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "encode search request", "search", map[string]any{"endpoint": endpoint}, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "build search request", "search", map[string]any{"endpoint": endpoint}, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "search provider request failed", "search", map[string]any{"endpoint": endpoint}, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBodySize))
	if err != nil {
		return nil, response.StatusCode, toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "read search provider response", "search", map[string]any{"endpoint": endpoint}, err)
	}
	return data, response.StatusCode, nil
}

func decodeJSONError(endpoint string, err error) *toolcore.ToolError {
	return toolcore.NewErrorCause("SEARCH_PROVIDER_FAILED", "search provider returned invalid JSON", "search", map[string]any{"endpoint": endpoint}, err)
}

func upstreamError(provider, detail string) *toolcore.ToolError {
	return toolcore.NewErrorDetails(
		"SEARCH_PROVIDER_FAILED",
		fmt.Sprintf("search provider %q failed: %s", provider, detail),
		"search",
		map[string]any{"provider": provider},
	)
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
