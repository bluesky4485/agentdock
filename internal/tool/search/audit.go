package search

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/uvwt/agentdock/internal/observability"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

const (
	auditFileMode   = 0o600
	auditRotateSize = 10 << 20
)

// AuditEntry 是政务合规要求的全量审计记录。caller 关联走 otel trace：
// hub 在 ToolInvoke 消息上注入 traceparent，节点侧工具执行时读取 span trace id，
// hub 侧即可用同一 trace id 关联"哪个用户/会话触发了这次外发"。
type AuditEntry struct {
	TS            string `json:"ts"`
	Tool          string `json:"tool"`
	Provider      string `json:"provider,omitempty"`
	Query         string `json:"query,omitempty"`
	URL           string `json:"url,omitempty"`
	Source        string `json:"source,omitempty"`
	TraceID       string `json:"trace_id,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
	ResultCount   int    `json:"result_count,omitempty"`
	Outcome       string `json:"outcome"`
	Engine        string `json:"engine,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type auditLogger struct {
	mu   sync.Mutex
	path string
}

func newAuditLogger(home string) *auditLogger {
	if strings.TrimSpace(home) == "" {
		return &auditLogger{}
	}
	return &auditLogger{path: filepath.Join(home, "logs", "search-audit.log")}
}

func (l *auditLogger) Log(entry AuditEntry) {
	if l == nil || l.path == "" {
		return
	}
	if entry.TS == "" {
		entry.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(data)) > auditRotateSize {
		_ = os.Rename(l.path, l.path+".1")
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, auditFileMode)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(data)
}

func auditCode(err error) string {
	for err != nil {
		if typed, ok := err.(*toolcore.ToolError); ok {
			return typed.Code
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = unwrapper.Unwrap()
	}
	return ""
}

// auditContext 从调用上下文提取审计维度：来源（mcp/nexus/internal）与 otel trace id。
func auditContext(ctx context.Context) (string, string) {
	source := ""
	traceID := ""
	if ctx != nil {
		source = string(observability.SourceFromContext(ctx))
		if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
			traceID = spanContext.TraceID().String()
		}
	}
	return source, traceID
}
