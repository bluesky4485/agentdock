package search

import (
	"context"
	"errors"
	"net"
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

func TestFetchFollowsCrossHostRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<div>other host</div>"))
	}))
	defer target.Close()
	// httptest 只绑定 127.0.0.1；用 localhost 构造真正跨主机名的重定向目标。
	// 同 IP 不同主机名属于跨域，设计上放行（文档站迁移域名的常态）。
	redirectTarget := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer source.Close()

	policy := allowLoopbackPolicy()
	policy.FetchAllowPrivateHosts = normalizeHosts([]string{"127.0.0.1", "localhost"})
	fetcher := newFetcher(policy)
	response, err := fetcher.Fetch(context.Background(), mustParse(t, source.URL))
	if err != nil {
		t.Fatalf("cross-host redirect must be followed: %v", err)
	}
	if response.FinalURL != redirectTarget || !strings.Contains(response.Text, "other host") {
		t.Fatalf("unexpected fetch envelope: %#v", response)
	}
}

func TestFetchRedirectTargetRecheckedAgainstPolicy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("blocked host"))
	}))
	defer target.Close()
	// 首跳域名在 allowlist 内，重定向目标不在：每一跳必须重新过域名策略。
	// httptest 目标解析到 127.0.0.1，拨号层放行清单必须同时覆盖两个主机名。
	redirectTarget := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer source.Close()
	sourceHost := mustParse(t, source.URL).Hostname()

	policy := &Policy{
		WebFetch:               &WebFetchPolicy{Enabled: true},
		FetchAllowPrivateHosts: normalizeHosts([]string{"127.0.0.1", "localhost"}),
		AllowedDomains:         []string{sourceHost},
	}
	fetcher := newFetcher(policy)
	_, err := fetcher.Fetch(context.Background(), mustParse(t, source.URL))
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "FETCH_REDIRECT_BLOCKED" {
		t.Fatalf("expected FETCH_REDIRECT_BLOCKED, got %v", err)
	}
	if !strings.Contains(err.Error(), "localhost") {
		t.Fatalf("error must name the rejected redirect target: %v", err)
	}
}

func TestFetchUpstreamFailureCarriesCause(t *testing.T) {
	// 拨号到一个立即关闭的端口，制造网络层失败。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := listener.Addr().String()
	_ = listener.Close()

	fetcher := newFetcher(allowLoopbackPolicy())
	_, fetchErr := fetcher.Fetch(context.Background(), mustParse(t, "http://"+closedPort))
	var toolErr *toolcore.ToolError
	if !errors.As(fetchErr, &toolErr) || toolErr.Code != "FETCH_UPSTREAM_FAILED" {
		t.Fatalf("expected FETCH_UPSTREAM_FAILED, got %v", fetchErr)
	}
	if !strings.Contains(toolErr.Message, "fetch request failed: ") || toolErr.Message == "fetch request failed: " {
		t.Fatalf("message must carry the underlying cause: %q", toolErr.Message)
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
