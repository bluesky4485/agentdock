package search

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

// config write/show 是 `agentdock search config` 子命令与 GUI 的持久化入口：
// 写路径复用 Load 校验后再以 0600 原子落盘；读路径对 key 强制脱敏（只暴露
// key_stored 布尔位，与 nexus device.json 的 DeviceTokenStored 纪律一致）。

const (
	providersFileMode  = 0o600
	policyFileMode     = 0o600
	maxConfigFileBytes = 1 << 20
)

func lockPathProviders(home string) string { return filepath.Join(home, "search", ".providers.lock") }
func lockPathPolicy(home string) string    { return filepath.Join(home, "search", ".policy.lock") }

// SaveProviders 校验后原子写 providers.json。
func SaveProviders(home string, file *ProvidersFile) error {
	if err := validateProviders(file); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxConfigFileBytes {
		return errConfigFileTooLarge()
	}
	release, err := filelock.Acquire(context.Background(), lockPathProviders(home))
	if err != nil {
		return err
	}
	defer release()
	return atomicfile.Write(providersPath(home), data, providersFileMode)
}

// SavePolicy 原子写 policy.json。
func SavePolicy(home string, policy *Policy) error {
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxConfigFileBytes {
		return errConfigFileTooLarge()
	}
	release, err := filelock.Acquire(context.Background(), lockPathPolicy(home))
	if err != nil {
		return err
	}
	defer release()
	return atomicfile.Write(policyPath(home), data, policyFileMode)
}

// providerView 是 show 子命令的脱敏投影：key 只出现"是否已配置"。
type providerView struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	KeyStored  bool   `json:"key_stored"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	IsDefault  bool   `json:"is_default,omitempty"`
	Credential bool   `json:"credential_ready"`
}

// ConfigView 是 show 子命令与 GUI 读取的脱敏配置视图。
type ConfigView struct {
	Providers       []providerView `json:"providers"`
	DefaultProvider string         `json:"default_provider,omitempty"`
	WebFetchEnabled bool           `json:"web_fetch_enabled"`
}

// View 把磁盘配置投影为脱敏视图，供 CLI show 与 GUI 读取。
func View(home string) (*ConfigView, error) {
	file, err := LoadProviders(home)
	if err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(home)
	if err != nil {
		return nil, err
	}
	out := &ConfigView{Providers: make([]providerView, 0, len(file.Providers)), DefaultProvider: file.DefaultProvider}
	out.WebFetchEnabled = policy.WebFetch != nil && policy.WebFetch.Enabled
	for _, cfg := range file.Providers {
		out.Providers = append(out.Providers, providerView{
			Name:       cfg.Name,
			Type:       cfg.Type,
			KeyStored:  cfg.APIKey != "" || (cfg.APIKeyEnv != "" && osLookupNonEmpty(cfg.APIKeyEnv)),
			APIKeyEnv:  cfg.APIKeyEnv,
			Endpoint:   cfg.Endpoint,
			IsDefault:  cfg.Name == file.DefaultProvider,
			Credential: cfg.CredentialReady(),
		})
	}
	return out, nil
}

func errConfigFileTooLarge() error {
	return errorString("search config file exceeds size limit")
}

type errorString string

func (e errorString) Error() string { return string(e) }

func osLookupNonEmpty(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}
