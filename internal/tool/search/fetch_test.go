package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func allowLoopbackPolicy() *Policy {
	return &Policy{WebFetch: &WebFetchPolicy{Enabled: true}, FetchAllowPrivateHosts: normalizeHosts([]string{"127.0.0.1"})}
}

func TestFetchExtractsHTMLTextSkippingScripts(t *testing.T) {
	page := `<!doctype html><html><head><title>页面标题</title><style>body{color:red}</style></head>
<body><h1>标题一</h1><script>alert("skip")</script><p>第一段 内容</p><div>第二段</div><a href="x">链接文字</a></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	fetcher := newFetcher(allowLoopbackPolicy())
	response, err := fetcher.Fetch(context.Background(), mustParse(t, server.URL))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if response.Status != http.StatusOK || response.FinalURL != server.URL {
		t.Fatalf("unexpected fetch envelope: %#v", response)
	}
	for _, want := range []string{"页面标题", "标题一", "第一段 内容", "第二段", "链接文字"} {
		if !strings.Contains(response.Text, want) {
			t.Fatalf("extracted text missing %q: %q", want, response.Text)
		}
	}
	if strings.Contains(response.Text, "alert") || strings.Contains(response.Text, "color:red") {
		t.Fatalf("script/style content leaked into text: %q", response.Text)
	}
}

func TestFetchPassesThroughJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"k": "v"}`))
	}))
	defer server.Close()
	fetcher := newFetcher(allowLoopbackPolicy())
	response, err := fetcher.Fetch(context.Background(), mustParse(t, server.URL))
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if response.Text != `{"k": "v"}` {
		t.Fatalf("JSON payload must pass through: %q", response.Text)
	}
}

func TestFetchRejectsUnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("binary"))
	}))
	defer server.Close()
	fetcher := newFetcher(allowLoopbackPolicy())
	_, err := fetcher.Fetch(context.Background(), mustParse(t, server.URL))
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "FETCH_CONTENT_TYPE_UNSUPPORTED" {
		t.Fatalf("expected FETCH_CONTENT_TYPE_UNSUPPORTED, got %v", err)
	}
}

func TestFetchRejectsCrossHostRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("other host"))
	}))
	defer target.Close()
	// httptest 只绑定 127.0.0.1；用 localhost 构造真正跨主机名的重定向目标。
	// 同 IP 不同端口属于同域，是允许的（设计：同域重定向）。
	redirectTarget := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer source.Close()

	fetcher := newFetcher(allowLoopbackPolicy())
	if _, err := fetcher.Fetch(context.Background(), mustParse(t, source.URL)); err == nil {
		t.Fatal("cross-host redirect must be rejected")
	}
}

func TestFetchRejectsLoopbackWithoutAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	fetcher := newFetcher(&Policy{WebFetch: &WebFetchPolicy{Enabled: true}})
	_, err := fetcher.Fetch(context.Background(), mustParse(t, server.URL))
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "FETCH_HOST_REJECTED" {
		t.Fatalf("expected FETCH_HOST_REJECTED, got %v", err)
	}
}

func TestFetchRejectsNonHTTPScheme(t *testing.T) {
	fetcher := newFetcher(allowLoopbackPolicy())
	if _, err := fetcher.Fetch(context.Background(), mustParse(t, "file:///etc/passwd")); err == nil {
		t.Fatal("non-http scheme must be rejected before any I/O")
	}
}

func TestExtractTextCollapsesWhitespace(t *testing.T) {
	text := extractText([]byte("<div>  a   b </div>\n<p>c</p>"), "text/html")
	if text != "a b\nc" {
		t.Fatalf("unexpected collapsed text: %q", text)
	}
}

func mustParse(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	return parsed
}
