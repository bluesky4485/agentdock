package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMacOSSearchConfigDecodeMatchesCoreContract 锁住 macOS 与 Core
// `search config show --json` 输出的 JSON 契约：Swift 模型必须显式映射
// snake_case 键，并对 omitempty 缺席字段（is_default=false）兜底解码。
// regression：字段名不匹配导致联网搜索设置页无论配置与否都报错。
func TestMacOSSearchConfigDecodeMatchesCoreContract(t *testing.T) {
	path := filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources", "SearchProviderConfiguration.swift")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SearchProviderConfiguration.swift: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`case apiKey = "api_key"`,
		`case hasKey = "key_stored"`,
		`case isDefault = "is_default"`,
		`case credentialReady = "credential_ready"`,
		`case defaultProvider = "default_provider"`,
		`case webFetchEnabled = "web_fetch_enabled"`,
		"decodeIfPresent",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("macOS search config model missing %q (must map Core snake_case contract)", want)
		}
	}
}
