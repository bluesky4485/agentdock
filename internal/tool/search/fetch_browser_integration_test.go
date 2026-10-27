package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// findChromePath 返回本机 Chromium 系浏览器路径；没有则跳过测试（Q4: 环境可能没有浏览器）。
func findChromePath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if _, err := exec.LookPath("chromium"); err == nil {
		return "chromium"
	}
	t.Skip("no Chromium-family browser on this machine; browser engine cannot be exercised end to end")
	return ""
}

func TestFetchBrowserEngineRendersThinPageEndToEnd(t *testing.T) {
	chromePath := findChromePath(t)
	// CSR 空壳：服务端只有挂载点，正文由 JS 渲染；HTTP 提取结果为空。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>csr page</title></head>
<body><div id="root"></div><script>
document.getElementById('root').textContent = 'browser rendered content ' + 'x'.repeat(600);
</script></body></html>`))
	}))
	defer server.Close()

	home := t.TempDir()
	writeSearchFile(t, home, "providers.json", `{"providers": []}`)
	writeSearchFile(t, home, "policy.json", `{"web_fetch": {"enabled": true}, "fetch_allow_private_hosts": ["127.0.0.1", "localhost"]}`)
	service, err := NewWithBrowser(home, chromePath)
	if err != nil {
		t.Fatalf("NewWithBrowser returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// engine=http：确认基线是薄文本（只有 title，没有 JS 渲染的正文）。
	httpResponse, err := service.Fetch(ctx, FetchRequest{URL: server.URL, Engine: "http"})
	if err != nil {
		t.Fatalf("http engine returned error: %v", err)
	}
	if httpResponse["render_engine"] != "http" || strings.Contains(asString(httpResponse["text"]), "browser rendered content") {
		t.Fatalf("http engine baseline must miss client-side content: %#v", httpResponse)
	}

	// engine=auto：HTTP 结果过薄，自动降级浏览器渲染。
	autoResponse, err := service.Fetch(ctx, FetchRequest{URL: server.URL, Engine: "auto"})
	if err != nil {
		t.Fatalf("auto engine returned error: %v", err)
	}
	if autoResponse["render_engine"] != "browser" {
		t.Fatalf("auto engine must fall back to browser for thin text: %#v", autoResponse)
	}
	if !strings.Contains(asString(autoResponse["text"]), "browser rendered content") {
		t.Fatalf("browser engine must render client-side content: %q", asString(autoResponse["text"]))
	}

	// engine=browser：显式请求浏览器渲染。
	browserResponse, err := service.Fetch(ctx, FetchRequest{URL: server.URL, Engine: "browser"})
	if err != nil {
		t.Fatalf("browser engine returned error: %v", err)
	}
	if browserResponse["render_engine"] != "browser" || !strings.Contains(asString(browserResponse["text"]), "browser rendered content") {
		t.Fatalf("explicit browser engine must render: %#v", browserResponse)
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
