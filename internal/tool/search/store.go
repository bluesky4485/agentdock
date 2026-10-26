package search

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// provider 类型。tavily/doubao/deepseek/exa/kimi/searchfree 为编译期适配器，
// http 为配置驱动的通用 REST provider（政务内网搜索网关等静态 key 场景）。
const (
	ProviderTypeTavily     = "tavily"
	ProviderTypeDoubao     = "doubao"
	ProviderTypeDeepSeek   = "deepseek"
	ProviderTypeExa        = "exa"
	ProviderTypeKimi       = "kimi"
	ProviderTypeSearchFree = "searchfree"
	ProviderTypeHTTP       = "http"
)

const (
	ConfigSourceLocal = "local"
	ConfigSourceHub   = "hub" // 预留：agenthub 集中下发。v1 只接受该字段值，不做同步实现。

	defaultTavilyEndpoint   = "https://api.tavily.com/search"
	defaultDoubaoEndpoint   = "https://open.feedcoopapi.com/search_api/global_search"
	defaultDeepSeekEndpoint = "https://api.deepseek.com/v1/tools/search"
	defaultExaEndpoint      = "https://api.exa.ai/search"
	defaultKimiEndpoint     = "https://api.moonshot.cn/v1/chat/completions"
	defaultSearchFree       = "https://searchfree.site/api/search"
)

var (
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	providerNameMatch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	envInterpolation  = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// ResponseMapping 描述 generic http provider 的响应字段提取路径。
// 路径是相对 item 的点分路径（如 "Url"、"DocumentInfo.PublishTime"）。
type ResponseMapping struct {
	ResultsPath string `json:"results_path,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Score       string `json:"score,omitempty"`
}

type ProviderConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// APIKey 是内联 key（GUI 配置场景），0600 文件直存；优先于 APIKeyEnv。
	APIKey          string            `json:"api_key,omitempty"`
	APIKeyEnv       string            `json:"api_key_env,omitempty"`
	Endpoint        string            `json:"endpoint,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	RequestTemplate map[string]any    `json:"request_template,omitempty"`
	ResponseMapping *ResponseMapping  `json:"response_mapping,omitempty"`
}

// CredentialReady 报告该 provider 是否"凭据就绪"：免 key 类型恒就绪；
// 内联 key 非空或 api_key_env 指向的环境变量非空视为就绪。
func (c ProviderConfig) CredentialReady() bool {
	if strings.TrimSpace(c.APIKeyEnv) == "" && strings.TrimSpace(c.APIKey) == "" {
		return true
	}
	if strings.TrimSpace(c.APIKey) != "" {
		return true
	}
	return os.Getenv(c.APIKeyEnv) != ""
}

// ProvidersFile 是 $AGENTDOCK_HOME/search/providers.json。
// 支持两种凭据形态：内联 api_key（GUI 配置，0600 文件直存）与 api_key_env
// 环境变量引用（CLI/运维场景）；内联优先。
type ProvidersFile struct {
	ConfigSource    string           `json:"config_source,omitempty"`
	DefaultProvider string           `json:"default_provider,omitempty"`
	Providers       []ProviderConfig `json:"providers"`
}

type WebFetchPolicy struct {
	Enabled bool `json:"enabled"`
}

// Policy 是 $AGENTDOCK_HOME/search/policy.json，同时约束 web_search 与 web_fetch。
type Policy struct {
	WebFetch               *WebFetchPolicy `json:"web_fetch,omitempty"`
	BlockedKeywords        []string        `json:"blocked_keywords,omitempty"`
	BlockedDomains         []string        `json:"blocked_domains,omitempty"`
	AllowedDomains         []string        `json:"allowed_domains,omitempty"`
	FetchAllowPrivateHosts []string        `json:"fetch_allow_private_hosts,omitempty"`
}

func providersPath(home string) string { return filepath.Join(home, "search", "providers.json") }
func policyPath(home string) string    { return filepath.Join(home, "search", "policy.json") }

// LoadProviders 读取 providers.json。文件不存在返回空配置（能力未启用，不报错）；
// 文件存在但非法返回错误，由 NewRuntime Preflight 快速失败。
func LoadProviders(home string) (*ProvidersFile, error) {
	file := &ProvidersFile{}
	if strings.TrimSpace(home) == "" {
		return file, nil
	}
	data, err := os.ReadFile(providersPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read search providers.json: %w", err)
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(file); err != nil {
		return nil, fmt.Errorf("decode search providers.json: %w", err)
	}
	if err := validateProviders(file); err != nil {
		return nil, err
	}
	return file, nil
}

func validateProviders(file *ProvidersFile) error {
	seen := make(map[string]struct{}, len(file.Providers))
	for i := range file.Providers {
		cfg := &file.Providers[i]
		cfg.Name = strings.TrimSpace(cfg.Name)
		cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))
		cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
		if cfg.Name == "" || !providerNameMatch.MatchString(cfg.Name) {
			return fmt.Errorf("search provider[%d] name %q is invalid", i, cfg.Name)
		}
		if _, exists := seen[cfg.Name]; exists {
			return fmt.Errorf("duplicate search provider %q", cfg.Name)
		}
		seen[cfg.Name] = struct{}{}
		switch cfg.Type {
		case ProviderTypeTavily, ProviderTypeDoubao, ProviderTypeDeepSeek, ProviderTypeExa, ProviderTypeKimi, ProviderTypeSearchFree:
		case ProviderTypeHTTP:
			if cfg.Endpoint == "" {
				return fmt.Errorf("search provider %q: type %q requires endpoint", cfg.Name, cfg.Type)
			}
			if _, err := url.Parse(cfg.Endpoint); err != nil || !strings.HasPrefix(cfg.Endpoint, "http") {
				return fmt.Errorf("search provider %q: endpoint %q is not a valid http(s) URL", cfg.Name, cfg.Endpoint)
			}
			if cfg.ResponseMapping == nil || strings.TrimSpace(cfg.ResponseMapping.Title) == "" || strings.TrimSpace(cfg.ResponseMapping.URL) == "" {
				return fmt.Errorf("search provider %q: generic http provider requires response_mapping with at least title and url paths", cfg.Name)
			}
		default:
			return fmt.Errorf("search provider %q: unsupported type %q", cfg.Name, cfg.Type)
		}
		if cfg.APIKeyEnv != "" && !envNamePattern.MatchString(cfg.APIKeyEnv) {
			return fmt.Errorf("search provider %q: api_key_env %q is invalid", cfg.Name, cfg.APIKeyEnv)
		}
	}
	if source := strings.TrimSpace(file.ConfigSource); source != "" && source != ConfigSourceLocal && source != ConfigSourceHub {
		return fmt.Errorf("search providers.json config_source %q is invalid", file.ConfigSource)
	}
	if file.DefaultProvider != "" {
		if _, ok := seen[strings.TrimSpace(file.DefaultProvider)]; !ok {
			return fmt.Errorf("search providers.json default_provider %q is not configured", file.DefaultProvider)
		}
	}
	return nil
}

// LoadPolicy 读取 policy.json。文件不存在返回默认策略（web_fetch 关闭、无拦截）。
func LoadPolicy(home string) (*Policy, error) {
	policy := &Policy{}
	if strings.TrimSpace(home) == "" {
		return policy, nil
	}
	data, err := os.ReadFile(policyPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read search policy.json: %w", err)
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(policy); err != nil {
		return nil, fmt.Errorf("decode search policy.json: %w", err)
	}
	policy.BlockedKeywords = normalizeList(policy.BlockedKeywords)
	policy.BlockedDomains = normalizeHosts(policy.BlockedDomains)
	policy.AllowedDomains = normalizeHosts(policy.AllowedDomains)
	policy.FetchAllowPrivateHosts = normalizeHosts(policy.FetchAllowPrivateHosts)
	return policy, nil
}

func normalizeList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeHosts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		host := strings.ToLower(strings.TrimSpace(value))
		host = strings.TrimSuffix(host, ".")
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}

// Preflight 在 NewRuntime 早期校验搜索配置。文件缺失视为未启用；
// 文件存在但非法立即报错，避免"配了但工具悄悄消失"。
func Preflight(home string) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	if _, err := LoadProviders(home); err != nil {
		return err
	}
	if _, err := LoadPolicy(home); err != nil {
		return err
	}
	return nil
}

// HasEnabledProvider 是 web_search 的 Availability 判定：
// ≥1 个 provider 条目处于"凭据就绪"状态（内联 key 非空、api_key_env 指向的环境
// 变量非空，或免 key 类型）。
func HasEnabledProvider(home string) bool {
	file, err := LoadProviders(home)
	if err != nil {
		return false
	}
	for _, cfg := range file.Providers {
		if cfg.CredentialReady() {
			return true
		}
	}
	return false
}

// FetchEnabled 是 web_fetch 的 Availability 判定：policy.json 显式开启。
func FetchEnabled(home string) bool {
	policy, err := LoadPolicy(home)
	if err != nil {
		return false
	}
	return policy.WebFetch != nil && policy.WebFetch.Enabled
}
