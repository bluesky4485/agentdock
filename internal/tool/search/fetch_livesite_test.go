package search

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFetchRealKimiDocsRedirect 是针对真实站点的回归验证：
// platform.moonshot.ai 的文档页 301 到 platform.kimi.ai（跨域），
// 旧版 web_fetch 因同域限制全部 FETCH_UPSTREAM_FAILED。
// 需要外网，无外网时跳过。
func TestFetchRealKimiDocsRedirect(t *testing.T) {
	if os.Getenv("TEST_REAL_SITES") == "" {
		t.Skip("set TEST_REAL_SITES=1 to run live-site regression")
	}

	home := t.TempDir()
	writeSearchFile(t, home, "providers.json", `{"providers": []}`)
	writeSearchFile(t, home, "policy.json", `{"web_fetch": {"enabled": true}}`)
	service, err := New(home)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	response, err := service.Fetch(ctx, FetchRequest{URL: "https://platform.moonshot.ai/docs/guide/kimi-k3-1-pro-quickstart"})
	if err != nil {
		t.Fatalf("cross-host redirect to kimi.ai must succeed: %v", err)
	}
	status, _ := response["status"].(float64)
	if status != 200 {
		t.Fatalf("unexpected status: %#v", response["status"])
	}
	if !strings.Contains(asString(response["final_url"]), "kimi.ai") {
		t.Fatalf("final URL must be the cross-host redirect target: %#v", response["final_url"])
	}
	if len(strings.TrimSpace(asString(response["text"]))) < thinTextThreshold {
		t.Fatalf("Kimi docs are SSR pages; extracted text must be substantial: %d chars", len(asString(response["text"])))
	}
}
