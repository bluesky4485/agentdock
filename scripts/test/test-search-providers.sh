#!/usr/bin/env bash
# 搜索提供商连通性测试脚本（政务网环境验证用）
#
# 逐个调用 tavily / doubao / deepseek / exa / kimi / searchfree 的真实 API，
# 验证：网络可达、API key 有效、响应可解析、返回结果条数。
# 与 AgentDock internal/tool/search 的适配器同源（endpoint/鉴权/参数一致），
# 本脚本通过 = 对应 provider 在 AgentDock 里可用。
#
# 用法：
#   1. export 需要测试的 key（只测配了 key 的源；不带参数 = 测全部已配置的）
#        export TAVILY_API_KEY=tvly-xxx
#        export DOUBAO_SEARCH_API_KEY=xxx
#        export DEEPSEEK_API_KEY=sk-xxx
#        export EXA_API_KEY=xxx
#        export MOONSHOT_API_KEY=sk-xxx
#   2. ./test-search-providers.sh [查询词]
#      查询词默认 "人工智能 政策"；政务网验证建议再用一个内部敏感词跑一遍
#      观察是否被网关拦截（这本身也是有价值的测试结论）。
#
# 退出码：全部通过的源 > 0 时 0；脚本自身跑完即 0。结果同时输出人读摘要。
set -u

QUERY="${1:-人工智能 政策}"
TIMEOUT=30

# ---------- 颜色 ----------
if [ -t 1 ]; then
  GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; OFF='\033[0m'
else
  GREEN=''; RED=''; YELLOW=''; CYAN=''; OFF=''
fi

PASS=0; FAIL=0; SKIP=0

have_key() { [ -n "${!1:-}" ]; }

# json 字段提取（不依赖 jq；优先 jq，回退 python3）
json_get() {
  local payload="$1" script="$2"
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$payload" | jq -r "$script" 2>/dev/null
  else
    printf '%s' "$payload" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    $script
except Exception:
    print('')
" 2>/dev/null
  fi
}

# 通用报告
report() {
  local name="$1" status="$2" detail="$3"
  case "$status" in
    ok)   printf "  ${GREEN}✓ 可用${OFF} %s\n" "$detail"; PASS=$((PASS+1)) ;;
    fail) printf "  ${RED}✗ 不可用${OFF} %s\n" "$detail"; FAIL=$((FAIL+1)) ;;
    skip) printf "  ${YELLOW}- 跳过${OFF} %s\n" "$detail"; SKIP=$((SKIP+1)) ;;
  esac
}

printf "${CYAN}═══ 搜索提供商连通性测试 ═══${OFF}\n"
printf "查询词: %s\n（政务网验证提示：也可用一个内部敏感词再跑一遍，观察网络是否放行/拦截）\n\n" "$QUERY"

# ---------- tavily ----------
printf "${CYAN}[1/6] tavily${OFF}  https://api.tavily.com/search\n"
if have_key TAVILY_API_KEY; then
  body=$(printf '{"query":"%s","max_results":3,"search_depth":"basic","include_answer":false}' "$QUERY")
  resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://api.tavily.com/search \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TAVILY_API_KEY" \
    -d "$body" 2>&1)
  code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
  if [ "$code" = "200" ]; then
    n=$(json_get "$payload" 'print(len(data.get("results") or []))' 2>/dev/null)
    [ -z "$n" ] && n=$(printf '%s' "$payload" | jq -r '(.results // []) | length' 2>/dev/null)
    report tavily ok "HTTP 200，返回 $n 条结果"
  else
    report tavily fail "HTTP ${code:-无响应}（网络不可达或 key 无效）：$(printf '%s' "$payload" | head -c 160)"
  fi
else
  report tavily skip "未 export TAVILY_API_KEY"
fi

# ---------- doubao ----------
printf "${CYAN}[2/6] doubao${OFF}  https://open.feedcoopapi.com/search_api/global_search\n"
if have_key DOUBAO_SEARCH_API_KEY; then
  body=$(printf '{"SearchType":"web","Query":"%s","DocCount":3,"MaxSnippetLength":1000}' "$QUERY")
  resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://open.feedcoopapi.com/search_api/global_search \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $DOUBAO_SEARCH_API_KEY" \
    -d "$body" 2>&1)
  code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
  if [ "$code" = "200" ]; then
    err_code=$(json_get "$payload" 'r=(data.get("Result") or {}).get("ErrorCode"); print("" if r in (None,0,"0") else r)')
    if [ -n "$err_code" ]; then
      report doubao fail "业务错误码 $err_code（key 无效或配额）：$(printf '%s' "$payload" | head -c 160)"
    else
      n=$(json_get "$payload" 'r=(data.get("Result") or {}).get("Documents") or []; print(len(r))')
      report doubao ok "HTTP 200，返回 $n 条结果"
    fi
  else
    report doubao fail "HTTP ${code:-无响应}：$(printf '%s' "$payload" | head -c 160)"
  fi
else
  report doubao skip "未 export DOUBAO_SEARCH_API_KEY"
fi

# ---------- deepseek ----------
printf "${CYAN}[3/6] deepseek${OFF}  https://api.deepseek.com/v1/tools/search\n"
if have_key DEEPSEEK_API_KEY; then
  body=$(printf '{"query":"%s","count":3,"need_content":false}' "$QUERY")
  resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://api.deepseek.com/v1/tools/search \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $DEEPSEEK_API_KEY" \
    -d "$body" 2>&1)
  code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
  if [ "$code" = "200" ]; then
    n=$(json_get "$payload" 'print(len(data.get("results") or []))')
    report deepseek ok "HTTP 200，返回 $n 条结果"
  else
    report deepseek fail "HTTP ${code:-无响应}：$(printf '%s' "$payload" | head -c 160)"
  fi
else
  report deepseek skip "未 export DEEPSEEK_API_KEY"
fi

# ---------- exa ----------
printf "${CYAN}[4/6] exa${OFF}  https://api.exa.ai/search\n"
if have_key EXA_API_KEY; then
  body=$(printf '{"query":"%s","numResults":3,"contents":{"highlights":true}}' "$QUERY")
  resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://api.exa.ai/search \
    -H 'Content-Type: application/json' \
    -H "x-api-key: $EXA_API_KEY" \
    -d "$body" 2>&1)
  code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
  if [ "$code" = "200" ]; then
    n=$(json_get "$payload" 'print(len(data.get("results") or []))')
    report exa ok "HTTP 200，返回 $n 条结果"
  else
    report exa fail "HTTP ${code:-无响应}：$(printf '%s' "$payload" | head -c 160)"
  fi
else
  report exa skip "未 export EXA_API_KEY"
fi

# ---------- kimi ----------
printf "${CYAN}[5/6] kimi${OFF}  https://api.moonshot.cn/v1/chat/completions（web_search 工具）\n"
if have_key MOONSHOT_API_KEY; then
  body=$(printf '{"model":"moonshot-v1-128k","messages":[{"role":"user","content":"%s"}],"tools":[{"type":"web_search"}],"temperature":0.3}' "$QUERY")
  resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://api.moonshot.cn/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $MOONSHOT_API_KEY" \
    -d "$body" 2>&1)
  code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
  if [ "$code" = "200" ]; then
    content=$(json_get "$payload" 'print((data["choices"][0]["message"].get("content") or "")[:80])')
    if [ -n "$content" ]; then
      report kimi ok "HTTP 200，合成答案：${content}…"
    else
      report kimi fail "HTTP 200 但无答案内容：$(printf '%s' "$payload" | head -c 160)"
    fi
  else
    report kimi fail "HTTP ${code:-无响应}：$(printf '%s' "$payload" | head -c 160)"
  fi
else
  report kimi skip "未 export MOONSHOT_API_KEY"
fi

# ---------- searchfree ----------
printf "${CYAN}[6/6] searchfree${OFF}  https://searchfree.site/api/search（免 key）\n"
body=$(printf '{"query":"%s","max_results":3,"search_depth":"basic"}' "$QUERY")
resp=$(curl -sS -m $TIMEOUT -w '\n%{http_code}' -X POST https://searchfree.site/api/search \
  -H 'Content-Type: application/json' \
  -d "$body" 2>&1)
code=$(printf '%s' "$resp" | tail -1); payload=$(printf '%s' "$resp" | sed '$d')
if [ "$code" = "200" ]; then
  n=$(json_get "$payload" 'r=data.get("results") or []; print(len(r) if isinstance(r,list) else 0)')
  report searchfree ok "HTTP 200，返回 $n 条结果"
else
  report searchfree fail "HTTP ${code:-无响应}（第三方免费源，可能限流）"
fi

# ---------- 基础网络探测（失败时的定位辅助） ----------
printf "\n${CYAN}── 网络定位辅助（仅在上述有失败时参考）──${OFF}\n"
probe() {
  if curl -sS -m 8 -o /dev/null "$1" 2>/dev/null; then
    printf "  ✓ %s 可达\n" "$1"
  else
    printf "  ✗ %s 不可达（网络层被拦或 DNS 失败）\n" "$1"
  fi
}
probe https://api.tavily.com
probe https://open.feedcoopapi.com
probe https://api.deepseek.com
probe https://api.exa.ai
probe https://api.moonshot.cn

printf "\n${CYAN}═══ 汇总 ═══${OFF}  可用: ${GREEN}${PASS}${OFF}  不可用: ${RED}${FAIL}${OFF}  跳过: ${YELLOW}${SKIP}${OFF}\n"
printf "对应关系：脚本通过的 provider，在 AgentDock 的 %s/search/providers.json 里配置同名 type 即可用。\n" "${AGENTDOCK_HOME:-\$AGENTDOCK_HOME}"
exit 0
