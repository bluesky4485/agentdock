# 政务终端 Web 搜索能力：决策记录与实施计划

> 状态：**Phase 1 + Phase 2 代码已实现并通过全量测试**（2026-10-25）；Phase 0 环境验证与 Phase 3 部署联调未做。
> 场景：政务环境服务器区网络受限，搜索能力由信创终端（Kylin/UOS）本地执行，
> 通过 AgentDock 内置工具 + agenthub（自建多用户 hub，基于 agentdock-protocol）中转给服务器侧 web agent（octop）。

## 总体架构

```
[服务器区] octop(web agent) ──MCP──> agenthub(自建NexusDock兼容hub)
                                        ▲ 终端主动 WSS 外连（Nexus Bridge，设备token）
                              [信创终端×N] AgentDock
                                        ├─ web_search ─> 搜索API(tavily/豆包/deepseek/…) ─> 互联网
                                        └─ web_fetch ──> 目标网页 ─> 互联网
```

- 链路方向：终端 → hub 单向外连（政务网默认放行方向），服务器区到终端无入站要求。
- 终端 AgentDock 工具面可收敛到仅 `web_search` / `web_fetch`（配置门控）。
- octop 侧政务部署需关闭本地 web_search 工具（`web_search_tools=False`），
  否则会在服务器区直连搜索 API 全部超时。

## 一、决策记录

| # | 决策 | 结论 | 理由 |
|---|------|------|------|
| D1 | 能力载体 | AgentDock 内置 Go 工具，非外挂 MCP 进程 | 纯 Go 静态编译适配信创离线环境；单二进制；审计/门控/契约复用现有框架；stdio 型 MCP（npx/uvx）在信创离线机装不起来 |
| D2 | Provider 抽象 | A3 混合：编译期适配器 + 配置驱动 generic HTTP | octop 六个源全部是"静态 key + REST"，适配器层薄（每个 60–100 行）；generic HTTP 留给政务内网搜索网关（加源不改代码） |
| D3 | Provider 清单 | tavily / doubao / deepseek / exa / kimi 移植 + searchfree 显式启用 | 对齐 octop `octop-harness/builtin/tools/web_search/_registry.py`；env 变量名沿用 octop（`TAVILY_API_KEY`、`DOUBAO_SEARCH_API_KEY`、`DEEPSEEK_API_KEY`、`EXA_API_KEY`、`MOONSHOT_API_KEY`），两端运维文档共用 |
| D4 | 工具面 | 单一 `web_search` + 可选 `provider` 参数；`web_fetch` 独立工具 | 避免加源改契约；provider 列表不进 schema enum，描述里注明"以节点配置为准，未配置的报错" |
| D5 | web_search / web_fetch 配套 | v1 一起交付 | octop 服务器区无替代抓取路径，"搜到读不了"链路不完整 |
| D6 | 结果 schema | 统一 `{title,url,snippet,published_at?,score?}` + 保留 tavily `answer?` 字段 | 对 LLM 无害；丢 answer 削 tavily 能力 |
| D7 | 失败语义 | 结构化报错给 agent（错误带可用 provider 列表），v1 不做终端侧 fallback 链 | 简单、可审计；政务要求"每次外发查了哪个源"清晰 |
| D8 | 凭据 | 环境变量存放；`providers.json` 只存 `api_key_env` 引用；预留 `config_source: local\|hub` 字段位 | v1 终端本地静态配置；为 agenthub 将来集中下发留口子，协议不动两次 |
| D9 | 合规层 | `policy.json`（blocked_keywords / allowed_domains / blocked_domains）+ 全量审计日志 + fetch 默认拒绝 loopback/内网地址（SSRF） | 搜索词出互联网是政务评审第一关注点；审计含 hub 侧 caller（走 Nexus bridge trace_context 透传） |
| D10 | searchfree（免key兜底源） | 移植，但改显式启用，政务 profile 默认关 | 免配置爬取源绕过 key 管控，政务环境不默认开 |
| D11 | web_fetch v1 范围 | 仅 HTML→正文文本，上限 1MB / 15s，同域重定向；PDF 进 v2 | octop 场景若常读政府 PDF 公文再提前 |
| D12 | hub 侧 | agenthub 已就绪，协议 v1 无改动 | 自建 hub 基于 agentdock-protocol，pair=1 个 HTTP 接口 + WS 五种消息 |

## 二、契约草案

### web_search

```jsonc
// input
{ "query": "string (required)", "provider": "string (optional)", "max_results": "int (optional, default 5)" }
// output
{
  "provider_used": "tavily",
  "answer": "string (optional, 仅 tavily 等支持合成的源返回)",
  "results": [ { "title": "", "url": "", "snippet": "", "published_at": "", "score": 0 } ]
}
// 错误：结构化，message 含当前可用 provider 列表
```

### web_fetch

```jsonc
// input
{ "url": "string (required, http/https)" }
// output
{ "final_url": "", "status": 200, "content_type": "text/html", "text": "…", "truncated": false }
// 约束：1MB / 15s / 同域重定向；默认拒绝 loopback、私网、链路本地地址（可配置放行清单）
// 门控：policy.json "web_fetch": {"enabled": true} 显式开启；默认关闭
```

### 默认不启用语义（钉死）

- 两个工具在出厂/未配置状态下**不是"存在但报错"，而是不存在**：`compileAvailableToolContracts`
  在启动时按 `Availability` 过滤，未配置的工具不进 `toolNames` / 契约 / tool_descriptors，
  因此 `/mcp`、agenthub 握手 Hello、octop 侧 LLM 的工具列表里均不可见。
- `web_search` 可用判定：providers.json 中 ≥1 个 provider 条目 **且** 其 `api_key_env`
  指向的环境变量非空（generic http 类型要求其引用的 header 变量可解析）。全都不满足 = 工具不存在。
- `web_fetch` 可用判定：policy.json 显式 `"web_fetch": {"enabled": true}`，默认关。
- 配置变更需重启 Core 生效（与 AgentDock 其余配置一致）。

### 配置文件（`$AGENTDOCK_HOME/search/`）

```jsonc
// providers.json
{
  "config_source": "local",          // 预留: "hub"
  "default_provider": "tavily",
  "providers": [
    { "name": "tavily",  "type": "tavily",  "api_key_env": "TAVILY_API_KEY" },
    { "name": "doubao",  "type": "doubao",  "api_key_env": "DOUBAO_SEARCH_API_KEY" },
    { "name": "deepseek","type": "deepseek","api_key_env": "DEEPSEEK_API_KEY" },
    { "name": "gov-intranet", "type": "http",
      "endpoint": "https://gov-search.internal/api",
      "headers": { "X-Api-Key": "${GOV_SEARCH_KEY}" },
      "request_template": { "q": "{{query}}", "n": "{{max_results}}" },
      "response_mapping": { "results_path": "data.list", "title": "Title", "url": "Url", "snippet": "Abstract" } }
  ]
}

// policy.json
{
  "web_fetch": { "enabled": true },   // web_fetch 总开关，默认缺省 = 关
  "blocked_keywords": ["…"],
  "blocked_domains": ["…"],
  "allowed_domains": [],              // 空 = 不限
  "fetch_allow_private_hosts": []     // SSRF 放行清单
}
```

### 审计日志

JSON 行，`$AGENTDOCK_HOME/logs/search-audit.log`（按大小轮转）：
`ts, tool, provider, query|url, caller(hub用户/会话, 经 trace_context), duration_ms, result_count, blocked_reason`

## 三、任务清单

> **实现进度（2026-10-25）**：T3–T11 已完成。代码落点：`internal/tool/search/`
> （contract/types/store/provider/providers_builtin/provider_http/policy/audit/service/fetch）+
> app 层接线（`specs_search.go`、`specs_contract.go`、`specs.go` 门控、`specs_registry.go`、`runtime.go`）。
> 与设计的两处实现细化：① 审计 caller 关联落地为 otel trace id + source（hub 在 ToolInvoke
> 注入 traceparent，用同一 trace id 关联用户/会话）；② providers.json/policy.json 非法时
> `toolsearch.New` 直接报错使 NewRuntime 失败（文件缺失=能力关闭），避免"配错但工具静默消失"。
> 全量 `go test ./...` 59 包通过；T1/T2（Phase 0）与 T12–T15（Phase 3）待政务现场执行。

### Phase 0 — 环境验证（前置，不写代码）

| # | 任务 | 验收 |
|---|------|------|
| T1 | 政务网连通性双向验证：服务器区→终端（预期不通，定案走外连方案）；终端→agenthub 端口（预期通）；终端→搜索 API 出口（确认可达） | 三条 curl 结论书面记录 |
| T2 | 信创终端基线：离线部署 AgentDock + `agentdock nexus pair --endpoint <agenthub>` 配对成功，hub 侧能看到节点工具面 | hub 节点列表可见、状态在线 |

### Phase 1 — web_search（fork 改动主体）

| # | 任务 | 验收 |
|---|------|------|
| T3 | `internal/tool/search` 包骨架：Provider 接口、编译期注册表、providers.json 加载器（含 `config_source` 预留字段） | 加载器单测覆盖合法/非法配置 |
| T4 | 五个适配器：tavily / doubao(`open.feedcoopapi.com/search_api/global_search`) / deepseek(`api.deepseek.com/v1/tools/search`) / exa / kimi，参照 octop 对应 Python 实现移植，输出归一化 | 每个适配器 httptest mock 单测通过 |
| T5 | generic HTTP provider：endpoint + 静态头 + 请求模板 + 响应字段映射 | 用政务内网网关假接口联调通过 |
| T6 | `web_search` spec 接线：input/output schema、`Availability`（≥1 provider 配好 key）、`typedToolHandler`；过 `compileAvailableToolContracts` 与 contract drift 测试 | `go test ./internal/app/...` 全绿 |
| T7 | 策略层：policy.json 加载、blocked_keywords 命中即拒、域名约束 | 拦截单测（命中/未命中/空配置） |
| T8 | 审计日志：JSON 行 + 轮转 + caller 字段（复用 nexusbridge trace_context） | 一次真实调用落一条完整记录 |
| T9 | 集成点收尾：config 新增字段、env 名对齐 octop、`agentdock_context` 能力清单体现 | 手动验收清单过一遍 |

### Phase 2 — web_fetch

| # | 任务 | 验收 |
|---|------|------|
| T10 | fetcher：GET、1MB/15s 上限、同域重定向、SSRF 防护（默认拒 loopback/私网，`fetch_allow_private_hosts` 放行）、HTML→正文提取（依赖尽量收敛到标准库+单一小依赖，保证信创离线可编译） | 恶意 URL 集合单测（127.0.0.1/169.254.196.254/10.x/重定向到私网）全拦截 |
| T11 | `web_fetch` spec + 门控 + 审计复用 + 单测 | 同 T6 标准 |

### Phase 3 — 集成、部署与联调

| # | 任务 | 验收 |
|---|------|------|
| T12 | 政务 profile：providers.json / policy.json 样例、systemd 环境变量注入、工具面最小化（只留两个 web 工具）文档 | 新终端按文档 30 分钟内完成接入 |
| T13 | 信创离线构建：交叉编译 arm64 / loong64，离线安装包与校验更新 | Kylin/UOS 实机安装通过 |
| T14 | octop 侧关闭本地搜索工具：per-agent 配置写死 `web_search_tools: false`（保底）；是否增加部署级强制覆盖开关为**待办 O1**，配置方式与代码事实详见 §五 | octop agent 工具列表无任何本地搜索工具；不产生服务器区直连搜索的报错 |
| T15 | 端到端联调：octop → agenthub → 终端 web_search/web_fetch → 互联网；核查审计日志 caller 链路；演练 key 缺失 / 超时 / 关键词拦截三类失败 | 全链路演示 + 审计抽样合规 |

### v2 Backlog（本计划不实现）

- PDF 公文文本提取（视 octop 场景优先级提前）
- provider fallback 链（有运行数据后再议，见 D7）
- `config_source=hub`：agenthub 集中下发 provider 配置
- agenthub key 托管 / 短时凭据签发（降低每终端持全量 key 的泄露面）

## 四、GUI 配置界面（2026-10-26 已实施）

**已实现**（用户拍板：agenthub 不下发，搜索完全客户端配置，key 直存配置文件；服务器侧搜索需求由 octop 自行解决）：

- **Core（Go）**：`providers.json` 新增内联 `api_key` 字段（0600 原子写，解析顺序内联 >
  `api_key_env`，向后兼容）；新 CLI `agentdock search config show/set`（show 输出强制脱敏为
  `key_stored` 布尔位；set 支持 `--stdin` 文档协议——providers 数组形态 + `web_fetch_enabled`
  开关，GUI 传 key 不进进程参数）；`RuntimeStatus` 增加 `search_providers`/`web_fetch_enabled`。
- **Windows WinUI**：能力页新增 Web Search 卡片（总开关、provider 列表编辑：类型下拉 +
  PasswordBox + 增删、web_fetch 开关）；key 走临时文件 + `--stdin`；保存后随既有流程重启 Core。
- **macOS**：设置新增"Web search"二级页（`SearchProvidersSettingsView`），模型在
  `SearchProviderConfiguration.swift`，保存走生命周期事务（含重启与既有回滚模式）；
  en/zh-Hans 双语 key 已过 `check-macos-i18n.py`；`test-macos-app.sh` swiftc 清单已登记。
- **GUI 编辑深度（拍板）**：仅内置六种 provider 类型 + key；generic http 类型与拦截词/
  域名策略不走 GUI（CLI 文件配置，见 china-os-deployment.md）。
- **验证边界**：Go 侧全量测试 + 端到端冒烟通过；macOS 本机仅 Command Line Tools
  （无 Xcode），SwiftUI 宏无法完整编译——parse 全清 + i18n 通过 + ServiceController 测试
  bundle 编译运行通过，完整 App 构建需在装 Xcode 的机器跑 `test-macos-app.sh`；WinUI 需
  Windows 实机验收。

## 五、风险与合规

1. **搜索词出互联网**：唯一红线。工具层 blocked_keywords + 全量审计是落地手段，制度上须约束 octop 侧 agent 不把内部数据拼进 query。
2. **key 泄露面**：每台终端持有 provider key。v1 接受（D8），v2 hub 托管后收敛。
3. **终端可用性**：多台终端冗余 + hub 侧健康可见；终端离线时 agent 侧报错降级。
4. **SSRF**：web_fetch 是终端内网→octop 会话的潜在探测跳板，T10 的拦截测试是硬验收。
5. **上游同步**：改动收敛为「一个新包 + 一份 spec 文件 + config 字段」，fork 同步 upstream 冲突面最小。

## 六、octop 侧 `web_search_tools` 配置方式（含待办）

### 已核实的代码事实（2026-10-25）

- 字段定义：`packages/octop-harness/src/octop_harness/config/__init__.py:772`，
  `HarnessAgentConfig.web_search_tools`，类型 `bool | "auto" | "all" | Sequence[str]`，
  **实际默认值是 `"auto"`**（`web_search/_registry.py` docstring 写的 `False` 已过时，勿信）。
- 工具加载点：`octop_harness/agent.py:1372`，`tools.extend(load_web_search_tools(cfg.web_search_tools))`。
- 工具目录门控：`src/octop/infra/agents/settings/tool_catalog.py:208`，
  `builtin_tool_available()` 仅在 agent 配置**显式** `web_search_tools: False` 时才认为不可用。
- dashboard 前端无该字段的专属 UI（grep 无命中），配置走 agent 配置本身。
- **关键坑**：`"auto"` 策略下 searchfree 免 env 变量、永远加载——不给 API key 并不能让
  octop 不挂搜索工具；政务服务器区里它会发起必然超时的出网请求，LLM 会反复重试。

### 可选的配置方式（按侵入度排序）

1. **per-agent 配置（保底，必须做）**：在 agent 的 HarnessAgentConfig 中设
   `web_search_tools: false`。落地途径：政务部署的 agent 创建模板 / 默认配置 JSON，
   确保每个新建 agent 自带此值；存量 agent 逐个更新。
2. **部署级强制覆盖（更稳，待拍板）**：新增部署级开关（如环境变量
   `OCTOP_WEB_SEARCH_TOOLS=false`），在 HarnessAgentConfig 组装/加载处对全部 agent
   兜底覆盖。一处配置防漏配，属 octop 侧小改动（用户自有代码库，可自行实施）。
3. **顺带修正**：把 `web_search/_registry.py` 的 docstring 默认值说明改为 `"auto"`，
   消除误导源。

### 待办（后续拍板，T14 执行前完成）

- **O1**：政务部署采用哪种机制——仅 per-agent 模板，还是加部署级覆盖开关
  （方式 2）。建议：方式 1 + 方式 2 同时做。
- **O2**：是否顺手修 `_registry.py` docstring（成本极低，建议做）。

## 七、相关代码位置

- octop provider 实现（移植蓝本）：`~/source2026/Octop/packages/octop-harness/src/octop_harness/builtin/tools/web_search/{tavily,doubao,deepseek,exa,kimi,searchfree}.py`
- AgentDock spec/门控模式：`internal/app/specs_recall.go`（`Availability: requiresNexus` 先例）、`internal/app/specs.go:128`（contract 编译）
- 工具契约常量惯例：`internal/tool/*/contract.go`（`域_动作` 命名）
- Nexus bridge 外连与 trace 透传：`internal/nexusbridge/client.go`、`internal/nexusbridge/trace_context.go`
- 配对命令：`cmd/agentdock/command_nexus.go`（`nexus pair --endpoint`）
