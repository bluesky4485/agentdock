package search

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolcontract "github.com/uvwt/agentdock/internal/tool/contract"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func newTestService(t *testing.T, providersJSON, policyJSON string, upstream *httptest.Server) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	providers := strings.ReplaceAll(providersJSON, "__UPSTREAM__", upstream.URL)
	writeSearchFile(t, home, "providers.json", providers)
	if policyJSON != "" {
		writeSearchFile(t, home, "policy.json", policyJSON)
	}
	service, err := New(home)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, home
}

func readAudit(t *testing.T, home string) []AuditEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "logs", "search-audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	entries := []AuditEntry{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode audit entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestServiceSearchHappyPathAndAudit(t *testing.T) {
	server, received := postEcho(t, `{"answer": "", "results": [{"title": "T", "url": "https://a.example/1", "content": "c", "score": 0.4}]}`, http.StatusOK)
	t.Setenv("TEST_SERVICE_TAVILY_KEY", "k")
	service, home := newTestService(t,
		`{"default_provider": "tavily", "providers": [{"name": "tavily", "type": "tavily", "api_key_env": "TEST_SERVICE_TAVILY_KEY", "endpoint": "__UPSTREAM__"}]}`,
		"", server)
	result, err := service.Search(context.Background(), SearchRequest{Query: "  hello world  "})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result["provider_used"] != "tavily" {
		t.Fatalf("unexpected provider_used: %#v", result["provider_used"])
	}
	var payload map[string]any
	_ = json.Unmarshal(*received, &payload)
	if payload["query"] != "hello world" {
		t.Fatalf("query must be trimmed: %#v", payload)
	}
	entries := readAudit(t, home)
	if len(entries) != 1 || entries[0].Outcome != "ok" || entries[0].Provider != "tavily" || entries[0].ResultCount != 1 {
		t.Fatalf("unexpected audit entries: %#v", entries)
	}
}

func TestServiceUnknownProviderListsAvailable(t *testing.T) {
	server, _ := postEcho(t, `{"results": []}`, http.StatusOK)
	t.Setenv("TEST_SERVICE_TAVILY_KEY", "k")
	service, _ := newTestService(t,
		`{"providers": [{"name": "tavily", "type": "tavily", "api_key_env": "TEST_SERVICE_TAVILY_KEY", "endpoint": "__UPSTREAM__"}]}`,
		"", server)
	_, err := service.Search(context.Background(), SearchRequest{Query: "q", Provider: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown provider must fail and mention the name: %v", err)
	}
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "SEARCH_PROVIDER_NOT_FOUND" {
		t.Fatalf("expected SEARCH_PROVIDER_NOT_FOUND, got %v", err)
	}
}

func TestServiceBlockedKeywordAuditsAndRejects(t *testing.T) {
	server, _ := postEcho(t, `{"results": []}`, http.StatusOK)
	t.Setenv("TEST_SERVICE_TAVILY_KEY", "k")
	service, home := newTestService(t,
		`{"providers": [{"name": "tavily", "type": "tavily", "api_key_env": "TEST_SERVICE_TAVILY_KEY", "endpoint": "__UPSTREAM__"}]}`,
		`{"blocked_keywords": ["内部资料"]}`, server)
	_, err := service.Search(context.Background(), SearchRequest{Query: "找一下内部资料"})
	if err == nil {
		t.Fatal("blocked keyword must reject the search")
	}
	entries := readAudit(t, home)
	if len(entries) != 1 || entries[0].Outcome != "blocked" || entries[0].BlockedReason == "" {
		t.Fatalf("blocked search must be audited: %#v", entries)
	}
}

func TestServiceFiltersBlockedResultDomains(t *testing.T) {
	server, _ := postEcho(t, `{"results": [
		{"title": "good", "url": "https://ok.example/1", "content": "c"},
		{"title": "bad", "url": "https://evil.example/2", "content": "c"}
	]}`, http.StatusOK)
	t.Setenv("TEST_SERVICE_TAVILY_KEY", "k")
	service, home := newTestService(t,
		`{"providers": [{"name": "tavily", "type": "tavily", "api_key_env": "TEST_SERVICE_TAVILY_KEY", "endpoint": "__UPSTREAM__"}]}`,
		`{"blocked_domains": ["evil.example"]}`, server)
	result, err := service.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	results, _ := result["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("blocked-domain results must be filtered: %#v", result["results"])
	}
	entries := readAudit(t, home)
	if entries[0].ResultCount != 1 {
		t.Fatalf("audit must record the post-filter count: %#v", entries[0])
	}
}

func TestServiceFetchRequiresEnabledPolicy(t *testing.T) {
	server, _ := postEcho(t, "ok", http.StatusOK)
	service, _ := newTestService(t, `{"providers": []}`, "", server)
	if _, err := service.Fetch(context.Background(), FetchRequest{URL: server.URL}); err == nil {
		t.Fatal("web_fetch must be rejected while policy leaves it disabled")
	}
}

func TestServiceFetchBlockedDomain(t *testing.T) {
	server, _ := postEcho(t, "ok", http.StatusOK)
	service, home := newTestService(t, `{"providers": []}`,
		`{"web_fetch": {"enabled": true}, "blocked_domains": ["127.0.0.1"]}`, server)
	_, err := service.Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err == nil {
		t.Fatal("blocked domain fetch must be rejected")
	}
	entries := readAudit(t, home)
	if len(entries) != 1 || entries[0].Outcome != "blocked" {
		t.Fatalf("blocked fetch must be audited: %#v", entries)
	}
}

func TestSearchSchemasCompileStrictly(t *testing.T) {
	for _, name := range []string{ToolWebSearch, ToolWebFetch} {
		input := InputSchema(name)
		if input["additionalProperties"] != false {
			t.Fatalf("%s input schema must reject unknown fields", name)
		}
		if _, err := toolcontract.CompileInputSchema(input); err != nil {
			t.Fatalf("compile %s input schema: %v", name, err)
		}
		if len(OutputSchema(name)) == 0 {
			t.Fatalf("%s output schema must not be empty", name)
		}
	}
	if InputSchema("nope") != nil || OutputSchema("nope") != nil {
		t.Fatal("unknown tool name must not yield a schema")
	}
}

func TestIsPublicIPGuards(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"127.0.0.1", false}, {"10.1.2.3", false}, {"172.16.0.9", false}, {"192.168.1.1", false},
		{"169.254.1.1", false}, {"0.0.0.0", false}, {"::1", false}, {"fe80::1", false},
		{"8.8.8.8", true}, {"114.114.114.114", true}, {"2606:4700::1111", true},
	}
	for _, item := range cases {
		ip := net.ParseIP(item.ip)
		if ip == nil {
			t.Fatalf("test bug: %s is not an IP", item.ip)
		}
		if got := isPublicIP(ip); got != item.public {
			t.Fatalf("isPublicIP(%s) = %v, want %v", item.ip, got, item.public)
		}
	}
}
