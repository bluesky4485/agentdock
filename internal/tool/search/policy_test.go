package search

import "testing"

func TestKeywordBlocked(t *testing.T) {
	policy := &Policy{BlockedKeywords: normalizeList([]string{"机密", "SECRET PROJECT"})}
	if _, blocked := policy.KeywordBlocked("please find 机密文件"); !blocked {
		t.Fatal("query containing blocked keyword must be blocked")
	}
	if _, blocked := policy.KeywordBlocked("anything about secret project x"); !blocked {
		t.Fatal("keyword matching must be case-insensitive")
	}
	if _, blocked := policy.KeywordBlocked("ordinary question"); blocked {
		t.Fatal("clean query must not be blocked")
	}
	empty := &Policy{}
	if _, blocked := empty.KeywordBlocked("anything"); blocked {
		t.Fatal("empty keyword list must not block")
	}
	(*Policy)(nil).KeywordBlocked("anything")
}

func TestDomainAllowed(t *testing.T) {
	blockedList := &Policy{BlockedDomains: normalizeHosts([]string{"evil.example", " leaked.cn "})}
	if _, ok := blockedList.DomainAllowed("evil.example"); ok {
		t.Fatal("exact blocked domain must be rejected")
	}
	if _, ok := blockedList.DomainAllowed("news.Evil.Example:443"); ok {
		t.Fatal("subdomain of blocked domain must be rejected")
	}
	if _, ok := blockedList.DomainAllowed("evilexample.com"); !ok {
		t.Fatal("suffix match must not span domain boundary: evilexample.com is not evil.example")
	}
	if _, ok := blockedList.DomainAllowed("good.example"); !ok {
		t.Fatal("unrelated domain must pass a blocklist-only policy")
	}

	allowList := &Policy{AllowedDomains: normalizeHosts([]string{"gov.cn"})}
	if _, ok := allowList.DomainAllowed("www.gov.cn"); !ok {
		t.Fatal("whitelisted domain must pass")
	}
	if _, ok := allowList.DomainAllowed("example.com"); ok {
		t.Fatal("non-whitelisted domain must be rejected when allow list is set")
	}
}

func TestHostAllowedByPolicy(t *testing.T) {
	policy := &Policy{FetchAllowPrivateHosts: normalizeHosts([]string{"archive.internal"})}
	if !policy.HostAllowedByPolicy("archive.internal") || !policy.HostAllowedByPolicy("Archive.Internal.") {
		t.Fatal("allowlisted private host must pass")
	}
	if policy.HostAllowedByPolicy("other.internal") {
		t.Fatal("non-allowlisted host must not pass")
	}
}
