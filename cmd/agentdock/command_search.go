package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/config"
	toolsearch "github.com/uvwt/agentdock/internal/tool/search"
)

// searchConfigStdinPayload 是 --stdin 的文档协议。Providers 既接受 provider
// 数组（GUI 形态），也接受整体 ProvidersFile 对象；WebFetchEnabled 是 GUI 的
// web_fetch 开关（映射到 policy.web_fetch.enabled）；Policy 提供时覆盖开关。
type searchConfigStdinPayload struct {
	Providers       json.RawMessage  `json:"providers"`
	DefaultProvider string           `json:"default_provider"`
	ConfigSource    string           `json:"config_source"`
	WebFetchEnabled *bool            `json:"web_fetch_enabled"`
	Policy          *json.RawMessage `json:"policy"`
}

func runSearchCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return searchCommandUsageError()
	}
	switch args[0] {
	case "config":
		return runSearchConfigCommand(ctx, args[1:], stdout, stderr)
	default:
		return searchCommandUsageError()
	}
}

func runSearchConfigCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return searchCommandUsageError()
	}
	switch args[0] {
	case "show":
		return runSearchConfigShowCommand(args[1:], stdout, stderr)
	case "set":
		return runSearchConfigSetCommand(ctx, args[1:], stdout, stderr)
	default:
		return searchCommandUsageError()
	}
}

func runSearchConfigShowCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("agentdock search config show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	home := flags.String("agentdock-home", "", "AgentDock state directory (must match Core AGENTDOCK_HOME)")
	// 输出本身就是 JSON；--json 为 GUI 调用契约保留的显式标记（接受即为兼容）。
	_ = flags.Bool("json", false, "print machine-readable JSON (default)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return searchCommandUsageError()
	}
	homeDir, err := resolveSearchHome(*home)
	if err != nil {
		return err
	}
	view, err := toolsearch.View(homeDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(view)
}

func runSearchConfigSetCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("agentdock search config set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	home := flags.String("agentdock-home", "", "AgentDock state directory (must match Core AGENTDOCK_HOME)")
	var providersJSON, policyJSON string
	flags.StringVar(&providersJSON, "providers-json", "", "providers.json content as JSON")
	flags.StringVar(&policyJSON, "policy-json", "", "policy.json content as JSON")
	fromStdin := flags.Bool("stdin", false, "read {\"providers\":…,\"policy\":…} JSON document from stdin (GUI path; avoids key exposure in process lists)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return searchCommandUsageError()
	}
	if *fromStdin {
		if strings.TrimSpace(providersJSON) != "" || strings.TrimSpace(policyJSON) != "" {
			return errors.New("--stdin 不能与 --providers-json/--policy-json 同时使用")
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
		if err != nil {
			return fmt.Errorf("读取 stdin: %w", err)
		}
		var payload searchConfigStdinPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("解码 stdin JSON: %w", err)
		}
		if len(payload.Providers) > 0 {
			providersJSON, err = normalizeStdinProviders(payload)
			if err != nil {
				return err
			}
		}
		if payload.Policy != nil {
			policyJSON = string(*payload.Policy)
		}
		if policyJSON == "" && payload.WebFetchEnabled != nil {
			// GUI 只编辑 web_fetch 总开关：保留既有拦截词/域名策略，只翻转开关。
			homeDir, homeErr := resolveSearchHome(*home)
			if homeErr != nil {
				return homeErr
			}
			existing, loadErr := toolsearch.LoadPolicy(homeDir)
			if loadErr != nil {
				return loadErr
			}
			if existing.WebFetch == nil {
				existing.WebFetch = &toolsearch.WebFetchPolicy{}
			}
			existing.WebFetch.Enabled = *payload.WebFetchEnabled
			encoded, encodeErr := json.Marshal(existing)
			if encodeErr != nil {
				return encodeErr
			}
			policyJSON = string(encoded)
		}
		if providersJSON == "" && policyJSON == "" {
			// 兼容裸文档形态：stdin 单独传 policy.json 的完整内容。
			var probe map[string]any
			if err := json.Unmarshal(data, &probe); err == nil {
				if _, hasWebFetch := probe["web_fetch"]; hasWebFetch || hasAnyKey(probe, "blocked_keywords", "blocked_domains", "allowed_domains", "fetch_allow_private_hosts") {
					policyJSON = string(data)
				}
			}
		}
	}
	if strings.TrimSpace(providersJSON) == "" && strings.TrimSpace(policyJSON) == "" {
		return errors.New("需要 --providers-json、--policy-json 或 --stdin 之一")
	}

	homeDir, err := resolveSearchHome(*home)
	if err != nil {
		return err
	}
	updated := false
	if strings.TrimSpace(providersJSON) != "" {
		file := &toolsearch.ProvidersFile{}
		if err := json.Unmarshal([]byte(providersJSON), file); err != nil {
			return fmt.Errorf("解码 providers JSON: %w", err)
		}
		if err := toolsearch.SaveProviders(homeDir, file); err != nil {
			return fmt.Errorf("保存 providers.json: %w", err)
		}
		updated = true
	}
	if strings.TrimSpace(policyJSON) != "" {
		policy := &toolsearch.Policy{}
		if err := json.Unmarshal([]byte(policyJSON), policy); err != nil {
			return fmt.Errorf("解码 policy JSON: %w", err)
		}
		if err := toolsearch.SavePolicy(homeDir, policy); err != nil {
			return fmt.Errorf("保存 policy.json: %w", err)
		}
		updated = true
	}
	if !updated {
		return errors.New("没有需要保存的配置")
	}
	fmt.Fprintln(stdout, `{"updated":true}`)
	fmt.Fprintln(stderr, "注意：搜索配置在 Core 重启后生效。")
	return nil
}

// resolveSearchHome 与 nexus 命令一致：默认取 config 归一化后的数据目录，
// --agentdock-home 显式覆盖（systemd 服务场景必须与服务进程一致）。
func resolveSearchHome(home string) (string, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) != "" {
		cfg.AgentDockHome = strings.TrimSpace(home)
	}
	if err := cfg.Normalize(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(cfg.AgentDockHome, "search"), 0o700); err != nil {
		return "", fmt.Errorf("准备搜索配置目录: %w", err)
	}
	return cfg.AgentDockHome, nil
}

func searchCommandUsageError() error {
	return errors.New("用法：agentdock search config <show [--json] [--agentdock-home <目录>] | set (--providers-json <json> | --policy-json <json> | --stdin) [--agentdock-home <目录>]>")
}

func hasAnyKey(document map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, exists := document[key]; exists {
			return true
		}
	}
	return false
}

// normalizeStdinProviders 把 stdin 的 providers 字段归一为 ProvidersFile JSON：
// 数组形态包装成文件对象；对象形态（含 default_provider/config_source）原样使用。
func normalizeStdinProviders(payload searchConfigStdinPayload) (string, error) {
	trimmed := bytes.TrimSpace(payload.Providers)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		wrapped, err := json.Marshal(map[string]any{
			"providers":        payload.Providers,
			"default_provider": payload.DefaultProvider,
			"config_source":    payload.ConfigSource,
		})
		if err != nil {
			return "", fmt.Errorf("编码 providers: %w", err)
		}
		return string(wrapped), nil
	}
	var probe map[string]any
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return "", fmt.Errorf("解码 providers: %w", err)
	}
	if _, hasProviders := probe["providers"]; !hasProviders {
		return "", errors.New(`stdin providers 字段必须是数组或含 "providers" 的对象`)
	}
	return string(trimmed), nil
}
