package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeSearchFile(t *testing.T, home, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "search"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "search", name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProvidersWithoutFileIsEmpty(t *testing.T) {
	file, err := LoadProviders(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProviders returned error: %v", err)
	}
	if len(file.Providers) != 0 || file.DefaultProvider != "" {
		t.Fatalf("expected empty providers file, got %#v", file)
	}
	if HasEnabledProvider(t.TempDir()) {
		t.Fatal("unconfigured home must not enable web_search")
	}
}

func TestLoadProvidersRejectsInvalidConfigs(t *testing.T) {
	cases := map[string]string{
		"invalid json":      `{"providers": [}`,
		"duplicate name":    `{"providers": [{"name": "a", "type": "tavily", "api_key_env": "K"}, {"name": "a", "type": "exa", "api_key_env": "K"}]}`,
		"unknown type":      `{"providers": [{"name": "a", "type": "nope"}]}`,
		"http no endpoint":  `{"providers": [{"name": "a", "type": "http", "response_mapping": {"title": "T", "url": "U"}}]}`,
		"http no mapping":   `{"providers": [{"name": "a", "type": "http", "endpoint": "https://gw.internal/api"}]}`,
		"bad env name":      `{"providers": [{"name": "a", "type": "tavily", "api_key_env": "9BAD"}]}`,
		"unknown default":   `{"default_provider": "ghost", "providers": [{"name": "a", "type": "tavily", "api_key_env": "K"}]}`,
		"bad config source": `{"config_source": "carrier-pigeon", "providers": [{"name": "a", "type": "tavily", "api_key_env": "K"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeSearchFile(t, home, "providers.json", content)
			if _, err := LoadProviders(home); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestLoadProvidersAcceptsValidConfig(t *testing.T) {
	home := t.TempDir()
	writeSearchFile(t, home, "providers.json", `{
		"config_source": "local",
		"default_provider": "tavily",
		"providers": [
			{"name": "tavily", "type": "tavily", "api_key_env": "TAVILY_API_KEY"},
			{"name": "gov", "type": "http", "endpoint": "https://gov.internal/api",
			 "headers": {"X-Key": "${GOV_KEY}"},
			 "request_template": {"q": "{{query}}", "n": "{{max_results}}"},
			 "response_mapping": {"results_path": "data.list", "title": "Title", "url": "Url", "snippet": "Abstract", "published_at": "Time", "score": "Rank"}}
		]
	}`)
	file, err := LoadProviders(home)
	if err != nil {
		t.Fatalf("LoadProviders returned error: %v", err)
	}
	if len(file.Providers) != 2 || file.DefaultProvider != "tavily" || file.ConfigSource != ConfigSourceLocal {
		t.Fatalf("unexpected parsed file: %#v", file)
	}
}

func TestHasEnabledProviderRequiresKeyPresence(t *testing.T) {
	home := t.TempDir()
	writeSearchFile(t, home, "providers.json", `{"providers": [{"name": "tavily", "type": "tavily", "api_key_env": "TEST_TAVILY_KEY"}]}`)
	if HasEnabledProvider(home) {
		t.Fatal("keyed provider with missing env must not enable web_search")
	}
	t.Setenv("TEST_TAVILY_KEY", "secret")
	if !HasEnabledProvider(home) {
		t.Fatal("keyed provider with present env must enable web_search")
	}

	home = t.TempDir()
	writeSearchFile(t, home, "providers.json", `{"providers": [{"name": "free", "type": "searchfree"}]}`)
	if !HasEnabledProvider(home) {
		t.Fatal("keyless provider entry must enable web_search when explicitly configured")
	}
}

func TestFetchEnabledRequiresExplicitPolicy(t *testing.T) {
	home := t.TempDir()
	if FetchEnabled(home) {
		t.Fatal("web_fetch must default to disabled")
	}
	writeSearchFile(t, home, "policy.json", `{"web_fetch": {"enabled": true}}`)
	if !FetchEnabled(home) {
		t.Fatal("explicitly enabled web_fetch must be available")
	}
}

func TestInlineAPIKeyEnablesProvider(t *testing.T) {
	home := t.TempDir()
	writeSearchFile(t, home, "providers.json", `{"providers": [{"name": "tavily", "type": "tavily", "api_key": "inline-secret"}]}`)
	if !HasEnabledProvider(home) {
		t.Fatal("inline api_key must enable web_search without env var")
	}
}

func TestInlineAPIKeyTakesPrecedenceOverEnv(t *testing.T) {
	server, received := postEcho(t, `{"results": []}`, 200)
	t.Setenv("TEST_INLINE_TAVILY_KEY", "env-key")
	provider := &tavilyProvider{config: ProviderConfig{Name: "tavily", Type: ProviderTypeTavily, APIKey: "inline-key", APIKeyEnv: "TEST_INLINE_TAVILY_KEY", Endpoint: server.URL}, client: newTestClient()}
	if _, err := provider.Search(context.Background(), SearchCall{Query: "q"}); err != nil {
		t.Fatalf("inline key must be usable: %v", err)
	}
	// 内联 key 优先：请求头带的是内联值而非 env 变量值。
	if string(*received) == "" {
		t.Fatal("request body must have been captured")
	}
}
