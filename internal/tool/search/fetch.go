package search

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

const (
	fetchTimeout      = 15 * time.Second
	fetchMaxBytes     = 1 << 20
	fetchMaxRedirects = 5
)

// fetcher 是 web_fetch 的执行器。SSRF 防护在拨号层强制执行：
// 连接前解析 DNS 并逐一校验解析出的 IP，防止域名解析到内网地址（DNS rebinding）。
type fetcher struct {
	policy *Policy
	client *http.Client
}

func newFetcher(policy *Policy) *fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			// 政务部署可将个别内网域名加入 fetch_allow_private_hosts 显式放行。
			if policy != nil && policy.HostAllowedByPolicy(host) {
				return dialer.DialContext(ctx, network, addr)
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve host %q: %w", host, err)
			}
			var chosen net.IP
			for _, item := range addresses {
				if isPublicIP(item.IP) {
					chosen = item.IP
					break
				}
			}
			if chosen == nil {
				return nil, toolcore.NewErrorDetails(
					"FETCH_HOST_REJECTED",
					fmt.Sprintf("host %q resolves only to loopback/private addresses and is blocked by policy", host),
					"search",
					map[string]any{"tool": ToolWebFetch},
				)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
		},
	}
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= fetchMaxRedirects {
			return fmt.Errorf("stopped after %d redirects", fetchMaxRedirects)
		}
		if normalizeHost(request.URL.Host) != normalizeHost(via[0].URL.Host) {
			return fmt.Errorf("redirect to a different host %q is not allowed", request.URL.Host)
		}
		if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
			return fmt.Errorf("redirect scheme %q is not allowed", request.URL.Scheme)
		}
		return nil
	}
	return &fetcher{policy: policy, client: client}
}

func (f *fetcher) Fetch(ctx context.Context, parsed *url.URL) (*FetchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	// IP 字面量在拨号层之前直接判定，给出更明确的错误；放行清单优先。
	if hostIP := net.ParseIP(parsed.Hostname()); hostIP != nil && !isPublicIP(hostIP) &&
		!(f.policy != nil && f.policy.HostAllowedByPolicy(parsed.Hostname())) {
		return nil, toolcore.NewErrorDetails(
			"FETCH_HOST_REJECTED",
			"loopback and private network hosts are blocked by policy",
			"search",
			map[string]any{"tool": ToolWebFetch},
		)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, toolcore.NewErrorCause("FETCH_URL_REJECTED", "build fetch request", "search", map[string]any{"tool": ToolWebFetch}, err)
	}
	request.Header.Set("User-Agent", "AgentDock-WebFetch/1.0")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.1")

	response, err := f.client.Do(request)
	if err != nil {
		return nil, toolcore.NewErrorCause("FETCH_UPSTREAM_FAILED", "fetch request failed", "search", map[string]any{"tool": ToolWebFetch}, err)
	}
	defer response.Body.Close()

	contentType := response.Header.Get("Content-Type")
	if !fetchableContentType(contentType) {
		return nil, toolcore.NewErrorDetails(
			"FETCH_CONTENT_TYPE_UNSUPPORTED",
			fmt.Sprintf("content type %q is not fetchable; web_fetch v1 supports text/html, text/plain, JSON and XML", contentType),
			"search",
			map[string]any{"tool": ToolWebFetch, "content_type": contentType},
		)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, fetchMaxBytes+1))
	if err != nil {
		return nil, toolcore.NewErrorCause("FETCH_UPSTREAM_FAILED", "read fetch response", "search", map[string]any{"tool": ToolWebFetch}, err)
	}
	truncated := len(data) > fetchMaxBytes
	if truncated {
		data = data[:fetchMaxBytes]
	}
	return &FetchResponse{
		FinalURL:    response.Request.URL.String(),
		Status:      response.StatusCode,
		ContentType: contentType,
		Text:        extractText(data, contentType),
		Truncated:   truncated,
	}, nil
}

func fetchableContentType(contentType string) bool {
	mediaType := strings.TrimSpace(strings.ToLower(strings.SplitN(contentType, ";", 2)[0]))
	if mediaType == "" {
		return true // 无 Content-Type 时按 HTML 尝试文本提取。
	}
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return true
	case mediaType == "application/xhtml+xml" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml"):
		return true
	default:
		return false
	}
}

// isPublicIP 判定"允许出网"的地址：排除 loopback、私网、链路本地、组播与未指定地址。
// net.IP 的判定方法对 IPv4/IPv4-mapped/IPv6 统一处理，不需要先做 To4/To16 归一。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

var (
	htmlSkipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "iframe": true}
	htmlBlocks   = map[string]bool{
		"p": true, "div": true, "br": true, "li": true, "tr": true, "table": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"section": true, "article": true, "header": true, "footer": true,
		"ul": true, "ol": true, "blockquote": true, "pre": true, "hr": true, "title": true,
	}
)

// extractText 对 HTML 走轻量 tokenizer 提取可见文本（跳过脚本/样式/模板），
// 其余文本类型原样返回。内容已在上游被截到 fetchMaxBytes。
func extractText(data []byte, contentType string) string {
	mediaType := strings.ToLower(strings.SplitN(contentType, ";", 2)[0])
	if mediaType != "" && !strings.Contains(mediaType, "html") && !strings.HasSuffix(mediaType, "+xml") && mediaType != "application/xml" {
		return strings.TrimSpace(string(data))
	}

	var builder strings.Builder
	skipDepth := 0
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}
		switch tokenType {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			if htmlSkipTags[tag] {
				if tokenType == html.StartTagToken && !htmlSelfClosing(tag) {
					skipDepth++
				}
				continue
			}
			if htmlBlocks[tag] {
				builder.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			if htmlSkipTags[tag] && skipDepth > 0 {
				skipDepth--
			}
			if htmlBlocks[tag] {
				builder.WriteByte('\n')
			}
		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			builder.Write(tokenizer.Text())
		}
	}
	return collapseWhitespace(builder.String())
}

func htmlSelfClosing(tag string) bool {
	return tag == "svg" || tag == "iframe"
}

func collapseWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
