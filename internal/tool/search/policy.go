package search

import "strings"

// KeywordBlocked 命中即拒：任一拦截词（大小写不敏感）出现在 query 中即返回该词。
func (p *Policy) KeywordBlocked(query string) (string, bool) {
	if p == nil || len(p.BlockedKeywords) == 0 {
		return "", false
	}
	normalized := strings.ToLower(query)
	for _, keyword := range p.BlockedKeywords {
		if strings.Contains(normalized, strings.ToLower(keyword)) {
			return keyword, true
		}
	}
	return "", false
}

// DomainAllowed 校验主机名：blocked_domains 命中即拒；allowed_domains 非空时
// 采用白名单制。域名匹配为精确或子域（example.com 匹配 a.example.com）。
func (p *Policy) DomainAllowed(host string) (string, bool) {
	normalized := normalizeHost(host)
	if normalized == "" {
		return "empty host", false
	}
	if p != nil {
		for _, domain := range p.BlockedDomains {
			if hostMatches(normalized, domain) {
				return "domain " + domain + " is blocked", false
			}
		}
		if len(p.AllowedDomains) > 0 {
			for _, domain := range p.AllowedDomains {
				if hostMatches(normalized, domain) {
					return "", true
				}
			}
			return "domain is not in the allow list", false
		}
	}
	return "", true
}

// HostAllowedByPolicy 是 fetch_allow_private_hosts 的主机级判定：
// 政务部署可把个别内网域名加入放行清单（放行后其解析 IP 允许访问）。
func (p *Policy) HostAllowedByPolicy(host string) bool {
	if p == nil {
		return false
	}
	normalized := normalizeHost(host)
	for _, domain := range p.FetchAllowPrivateHosts {
		if hostMatches(normalized, domain) {
			return true
		}
	}
	return false
}

func hostMatches(host, pattern string) bool {
	if pattern == "" {
		return false
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

// normalizeHost 小写化并去掉端口与结尾点，用于域名策略匹配。
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if index := strings.LastIndex(host, ":"); index >= 0 && !strings.Contains(host, "]") {
		host = host[:index]
	}
	if index := strings.Index(host, "]"); index >= 0 {
		host = strings.TrimPrefix(host[:index], "[")
	}
	return strings.TrimSuffix(host, ".")
}
