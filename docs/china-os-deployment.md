# 国产化系统（麒麟 / 统信等）部署运行指南

本文记录 AgentDock 在国产化操作系统和硬件平台上的兼容性结论、部署注意事项和已知限制。适用于麒麟（Kylin V10 及以上）、统信 UOS、openEuler 等基于 Linux 的国产发行版，以及 x86_64（海光、兆芯、Intel/AMD）和 aarch64（飞腾、鲲鹏）国产硬件平台。

## 兼容性结论

### 可以直接运行 GitHub Actions 构建的发布包

官方 release 产物 `agentdock_linux_amd64.tar.gz` 与 `agentdock_linux_arm64.tar.gz` 在上述系统上**直接运行、免编译**。原因是构建采用 `CGO_ENABLED=0` 纯静态编译（见 `.github/workflows/release.yml` 的 Linux build job），经实际验证：

- 产物二进制为 `statically linked` ELF，运行时不加载 `libc.so`、不依赖 `ld-linux` 动态链接器；
- 二进制内无任何 `GLIBC` 符号引用，**完全不存在 glibc 版本问题**——麒麟 V10（glibc 2.28）、统信 UOS（glibc 2.28/2.31）乃至更老的 glibc 环境都无差别。

唯一的系统层边界是**内核版本**：Go 运行时在个别路径使用 `faccessat2`（内核 5.8+），旧内核上会自动降级回退，不影响功能。现役国产系统的内核（4.19/5.4 及以上）均满足要求。

### 发布形态对国产系统的适配

- 发布物为 tar.gz 而非 rpm/deb，不依赖特定包管理器与发行版格式；
- `install.sh` 不读取 `/etc/os-release` 做发行版探测，对麒麟/统信黑盒兼容：安装到 `/opt/agentdock`、配置写入 `/etc/agentdock/agentdock.env`、创建专用系统用户并注册 systemd 服务；
- 架构归一化覆盖国产硬件：`x86_64 → amd64`、`aarch64 → arm64`（脚本 `normalize_arch()`）；
- 服务管理支持 systemd（麒麟/统信服务器版默认）与 OpenRC 两种，均无依赖时可用 `AGENTDOCK_SERVICE_MANAGER=none` 只装二进制不注册服务。

### 架构覆盖范围与例外

| 硬件平台 | 架构 | 发布包支持 |
| --- | --- | --- |
| 海光 / 兆芯 / Intel / AMD | x86_64 | ✅ `agentdock_linux_amd64.tar.gz` |
| 飞腾 FT-2000 / 鲲鹏 920 | aarch64 (ARMv8) | ✅ `agentdock_linux_arm64.tar.gz` |
| 龙芯（LoongArch / 旧 mips64el） | loongarch64 / mips64el | ❌ 不在构建矩阵，需用对应平台 Go 工具链自行 `go build ./cmd/agentdock` |
| 申威 | sw_64 | ❌ 同上 |

## 麒麟安全机制（kysec）

麒麟 V10 开启安全模式时会对未签名二进制加执行限制，静态编译的第三方二进制可能被拦——这是国产系统上部署第三方 Go 服务的**常见第一步**，症状是二进制权限正常但执行时报"Permission denied"或"Operation not permitted"。

处理方式：

1. 查看拦截记录确认是 kysec 而非文件权限：
   ```bash
   # 查看安全状态
   security-switch --get
   # 查看审计/拦截日志
   ausearch -m avc 2>/dev/null | tail
   ```
2. 两种调整方向（按政务环境的安全策略选择）：
   - 调整安全级别：`security-switch --set softmode`（宽松模式，需安全部门评估）；
   - 保持严格模式，给 AgentDock 安装目录加白名单或为二进制打上可信标签（参照所用麒麟版本 kysec 工具链的 `kysec_set` / secutil 用法）。
3. 白名单应覆盖的路径：`/opt/agentdock/bin/agentdock`、`/opt/agentdock/bin/cloudflared`（如启用隧道组件）、以及 systemd 服务实际拉起的可执行文件。

统信 UOS 侧对应机制为沙箱/签名管控（深度安全中心），处理思路相同：将安装目录加入可执行白名单。

## 离线（政务内网）部署

政务环境通常无法直连 GitHub。`install.sh` 本身只是引导器，真正执行安装的是二进制内的 Go 安装引擎（`agentdock install` 子命令）；脚本唯一需要联网的步骤是下载 tar.gz——包已在手时整段跳过。

**方式 A：install.sh + 本地包**：将 tar.gz 放到与脚本约定的下载位置同路径，或将脚本内 `DEFAULT_BASE_URL` 指向内网镜像源（fork 版脚本在 `scripts/install/install.sh` 顶部维护该变量），照常执行。

**方式 B：直接调用安装引擎（100% 离线，与 install.sh 等价）**：

Release 的 Linux tar.gz 内**只含** `bin/agentdock` 与 `share/agentdock/core-skills/`（4 个官方 Skill 包），**不含** `install.sh`——脚本是与发布包平级的独立资产。内网机器上既拿不到脚本、也不想给脚本配下载源时，直接调用二进制内的安装引擎（`agentdock install` 子命令）即可，它与 install.sh 走的是同一条代码路径（脚本只是引擎参数的编排器）。

完整流程（以 v1.2.5 amd64 为例，arm64 换包名即可）：

```bash
# 0. 校验包完整性（离线机上用拷入的 .sha256 文件）
sha256sum -c agentdock_linux_amd64.tar.gz.sha256

# 1. 解压到临时目录
tar -xzf agentdock_linux_amd64.tar.gz -C /tmp/ad-payload

# 2. 执行安装（参数组合来自 install.sh Linux 分支传给引擎的原样参数）
sudo /tmp/ad-payload/bin/agentdock install \
  --install-root /opt/agentdock \
  --runtime-root /etc/agentdock \
  --service-name agentdock \
  --service-user agentdock \
  --service-group agentdock \
  --service-manager systemd \
  --data-dir /srv/agentdock \
  --agentdock-home /srv/agentdock/.agentdock \
  --agentdock-default-dir /srv/agentdock/AgentDock \
  --systemd-dir /etc/systemd/system \
  --openrc-dir /etc/init.d \
  --payload-dir /tmp/ad-payload \
  --skill-bundle /tmp/ad-payload/share/agentdock/core-skills \
  --version v1.2.5

# 3. 确认认证 Token（安装引擎已自动生成强随机值，无需手工创建）
sudo grep AGENTDOCK_AUTH_TOKEN /etc/agentdock/agentdock.env
sudo chmod 600 /etc/agentdock/agentdock.env   # 引擎已收紧，抽查确认未被运维脚本放宽

# 4. 验证服务
systemctl status agentdock
curl -s http://127.0.0.1:8765/healthz          # {"ok":true,"version":"v1.2.5"}
```

安装结果（目录布局、env 文件、专用用户、systemd 服务）与 install.sh 一致。安装引擎自带事务与回滚，失败时 `agentdock install --repair --install-root /opt/agentdock` 可修复。

**关于认证 Token（MCP 访问密码）**：

- **首次安装自动生成** 32 字节随机 token 写入 `/etc/agentdock/agentdock.env`（0600），升级/重跑时保留已有值不覆盖；也可用 `--auth-token <值>` 指定（推荐改用环境变量 `AGENTDOCK_AUTH_TOKEN` 传入，避免 token 出现在 `/proc/<pid>/cmdline` 中被同机用户看到）。
- **token 的作用边界**：`/mcp` 与 Runtime API 等能力端点要求 `Authorization: Bearer <token>`；`/healthz` 与 Web 状态页是公开的（仅本机可见的回环监听）。
- **默认直跑（`./agentdock` 前台，监听 127.0.0.1）无认证**是设计允许的——回环监听外部访问不到；但把 `AGENTDOCK_HOST` 改为非回环地址（如 0.0.0.0）且未配置 token 时，**Core 会直接拒绝启动**（"non-loopback host requires AGENTDOCK_AUTH_TOKEN or OAuth"），不会裸奔。
- 手工轮换：编辑 env 文件中的 `AGENTDOCK_AUTH_TOKEN` 值（`openssl rand -hex 32` 生成）后 `systemctl restart agentdock`，同时更新所有调用方（Nexus 配对走设备 Token 体系不受影响，直连 `/mcp` 的 HTTP 客户端必须换新头）。
- 公网暴露（隧道）场景应改用 OAuth 模式（`AGENTDOCK_OAUTH_ENABLED=true` + `AGENTDOCK_OAUTH_PASSWORD` + `AGENTDOCK_OAUTH_TOKEN_SECRET`，install.sh `--tunnel quick/named` 会自动配齐），不要只用静态 Bearer Token。

**方式 C：临时试用**：`chmod +x bin/agentdock && ./agentdock` 前台直跑，数据落在当前用户 `~/.agentdock`，关终端即停。仅建议验证用途，正式部署走 A/B。

### 离线升级

- 离线环境**不要执行 `agentdock update`**（默认连 GitHub）；
- 升级时下载新版本 tar.gz，按方式 A/B 重跑安装即可，安装引擎自带事务与回滚（`agentdock install --repair` 可用于修复）。

## Linux 环境的能力边界

Linux 没有 GUI 控制面板——`desktop/` 下只有 macOS（Swift）与 Windows（WinUI/C#）实现。这是面向服务器/VPS 的设计取舍，不是功能缺失。Linux 上的管理面为：

- **CLI**：`agentdock service / install / nexus / skill / config` 等子命令；
- **Web 状态页**：浏览器访问 `http://127.0.0.1:8765/`（版本、认证状态、Plugin 数量）；
- **journald / 日志文件**：systemd 环境日志交 journald 管理。

### NexusDock 配对（仅 CLI）

```bash
# 关键：必须使用 Core 服务的数据目录，否则凭据写入当前用户 HOME、服务读不到、配对码被消耗
sudo -u agentdock agentdock nexus pair \
  --endpoint <NexusDock 地址> \
  --code <一次性配对码> \
  --agentdock-home /srv/agentdock/.agentdock

# 验证
sudo -u agentdock agentdock nexus status --agentdock-home /srv/agentdock/.agentdock
```

无人值守场景：安装前 export `NEXUS_MODE=official NEXUS_ENDPOINT=... NEXUS_PAIR_CODE=...`，install.sh 安装完成后自动配对。

### 内置 Web 搜索能力配置（仅 CLI）

`web_search` / `web_fetch` 是按配置门控的内置工具：**未配置时工具不出现在任何工具面**（`/mcp`、Nexus Bridge、LLM 工具列表均不可见），配置文件非法则 Core 启动失败。Linux 无 GUI，通过文件/CLI 配置，改完重启服务生效。

CLI 途径（`agentdock search config`，配置校验与 0600 原子写入由 Core 完成）：

```bash
# 读取当前配置（key 脱敏为 key_stored 布尔位）
agentdock search config show --agentdock-home /srv/agentdock/.agentdock

# 写入（stdin 文档协议，key 内联；set 后需重启服务）
echo '{"providers":[{"name":"tavily","type":"tavily","api_key":"tvly-xxx"}],"default_provider":"tavily","web_fetch_enabled":true}' \
  | sudo -u agentdock agentdock search config set --stdin --agentdock-home /srv/agentdock/.agentdock
sudo systemctl restart agentdock
```

直接编辑文件途径（含 generic http 类型与拦截词/域名策略等高级字段）：

```bash
# 1. 创建搜索配置（AGENTDOCK_HOME 以部署为准，安装脚本默认 /srv/agentdock/.agentdock）
sudo mkdir -p /srv/agentdock/.agentdock/search

# 2. providers.json：声明搜索服务商。key 本体不落盘，用 api_key_env 引用环境变量
sudo tee /srv/agentdock/.agentdock/search/providers.json > /dev/null << 'EOF'
{
  "config_source": "local",
  "default_provider": "tavily",
  "providers": [
    { "name": "tavily",   "type": "tavily",   "api_key_env": "TAVILY_API_KEY" },
    { "name": "doubao",   "type": "doubao",   "api_key_env": "DOUBAO_SEARCH_API_KEY" },
    { "name": "deepseek", "type": "deepseek", "api_key_env": "DEEPSEEK_API_KEY" },
    { "name": "gov",      "type": "http",
      "endpoint": "https://gov-search.internal.example/api",
      "headers": { "X-Api-Key": "${GOV_SEARCH_KEY}" },
      "request_template": { "q": "{{query}}", "n": "{{max_results}}" },
      "response_mapping": { "results_path": "data.list", "title": "Title", "url": "Url", "snippet": "Abstract", "published_at": "Time", "score": "Rank" } }
  ]
}
EOF

# 3. policy.json：合规策略。web_fetch 默认关闭，必须显式启用
sudo tee /srv/agentdock/.agentdock/search/policy.json > /dev/null << 'EOF'
{
  "web_fetch": { "enabled": true },
  "blocked_keywords": [],
  "blocked_domains": [],
  "allowed_domains": [],
  "fetch_allow_private_hosts": []
}
EOF

# 4. API key 写进 env 文件（0600，随 systemd 注入 Core 进程）
sudo tee -a /etc/agentdock/agentdock.env > /dev/null << 'EOF'
TAVILY_API_KEY=tvly-xxxxxxxx
# GOV_SEARCH_KEY=internal-gateway-key
EOF
sudo chmod 600 /etc/agentdock/agentdock.env

# 5. 重启生效并验证工具面
sudo systemctl restart agentdock
curl -s http://127.0.0.1:8765/healthz
# 状态页 http://127.0.0.1:8765/ 可确认 web_search / web_fetch 已出现在工具列表
```

说明：

- **免 key 源**（`type: "searchfree"`）同样必须显式写进 providers.json 才会出现；政务 profile 不建议启用。
- **generic http 类型**（上例 `gov`）面向内网搜索网关：endpoint + 静态头（支持 `${ENV}` 插值，引用变量缺失会启动报错）+ 请求模板 + 响应字段映射，加源只改配置不改代码。
- **域名策略**同时约束搜索结果过滤与 `web_fetch` 目标；`fetch_allow_private_hosts` 是 web_fetch 访问内网主机的唯一放行通道（默认拒绝 loopback/私网地址，防 SSRF）。
- **审计日志**：每次搜索/抓取（含被拦截的）落 JSON 行到 `$AGENTDOCK_HOME/logs/search-audit.log`，字段含 trace_id（与 hub 侧会话关联）、provider、耗时、拦截原因。
- 排错：配置文件 JSON 非法时 Core 启动失败，`journalctl -u agentdock -n 50` 可看到具体校验错误。

### 与桌面端（Windows/macOS）的差异速览

| 能力 | Windows | macOS | Linux |
| --- | --- | --- | --- |
| MCP 服务（shell/文件/Git/Skill/浏览器） | ✅ | ✅ | ✅ 一致 |
| GUI 控制面板/托盘 | ✅ | ✅ | ❌（CLI + Web 状态页） |
| 隧道（Cloudflare quick/named） | ✅ | ✅ | ✅ |
| NexusDock 配对 | GUI + CLI | GUI + CLI | 仅 CLI |
| Web 搜索（web_search/web_fetch） | GUI 能力页 + CLI | GUI 设置页 + CLI | 仅 CLI（见上文配置指南） |
| 浏览器自动化 | 宿主 Chrome | 宿主 Chrome | 建议走 Docker 内 Chromium；宿主无桌面时用无头模式，或经 `AGENTDOCK_BROWSER_EXECUTABLE_PATH` 指定国产 Chromium 内核浏览器（奇安信/UOS 浏览器等，CDP 协议理论兼容，需实测验证） |

## 国产化环境其他注意事项

1. **安全软件放行清单**：除 kysec 外，政务内网常见主机安全审计/杀毒软件（如奇安信天擎、安全加固组件）同样可能拦截未签名二进制或对外监听行为，部署前将 `/opt/agentdock`、监听端口（默认 8765）、`/usr/local/bin/agentdock` 链接加入放行清单。
2. **下载网络**：GitHub release 直连在国内常不稳定。服务器上建议手动下载 tar.gz + `.sha256` 后按上文离线方式安装；或给脚本配代理；fork 版可把 `DEFAULT_BASE_URL` 改为内网制品库。
3. **checksum 校验在离线机器上完成**：下载机校验 `sha256sum -c agentdock_linux_amd64.tar.gz.sha256` 通过后，再将文件与校验值一同拷入内网，保持传输链可审计。
4. **Docker 路线**：镜像内为 Debian + 静态二进制，麒麟/统信上的 Docker 及国产容器运行时（iSula 等）均可运行；离线环境用 `docker save`/`docker load` 搬运镜像。compose 文件宿主侧默认只绑 `127.0.0.1`，token 必填。
5. **浏览器自动化**：宿主机无桌面时浏览器工具需无头 Chromium；政务环境建议优先用 Docker `browser` 标签镜像（内含配置好的 Chromium），避免在宿主机安装浏览器引入的安全审计问题。
6. **时间同步**：OAuth 与 NexusDock 凭据校验依赖系统时间，内网无 NTP 外联时务必配置内网时间源，时间漂移会导致 token 校验失败。
7. **umask 与权限**：安装引擎会收紧关键文件权限（env 0600、数据目录 0700）；若站点统一 umask 策略较宽，安装后抽查 `/etc/agentdock/agentdock.env` 与 `/srv/agentdock/.agentdock` 的权限未被后续运维脚本放宽。
8. **无 sudo/特权受限场景**：无法创建系统用户或写 `/etc` 时，退化为用户级部署：`AGENTDOCK_SERVICE_MANAGER=none` 手动安装或方式 C 直跑，但运行身份变为个人账号，安全边界弱于专用用户，需在台账中注明。
