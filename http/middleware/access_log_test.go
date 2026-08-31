package middleware

import (
	"context"
	"gochen-runtime/http/nethttp"
	"gochen/observe/logging"
	"net/http/httptest"
	"sync"
	"testing"
)

type logEntry struct {
	level  string
	msg    string
	fields []logging.Field
}

type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLogger) Debug(_ context.Context, msg string, fields ...logging.Field) {
	l.add("debug", msg, fields...)
}
func (l *captureLogger) Info(_ context.Context, msg string, fields ...logging.Field) {
	l.add("info", msg, fields...)
}
func (l *captureLogger) Warn(_ context.Context, msg string, fields ...logging.Field) {
	l.add("warn", msg, fields...)
}
func (l *captureLogger) Error(_ context.Context, msg string, fields ...logging.Field) {
	l.add("error", msg, fields...)
}

func (l *captureLogger) WithFields(_ ...logging.Field) logging.ILogger { return l }
func (l *captureLogger) WithField(_ string, _ any) logging.ILogger     { return l }

func (l *captureLogger) add(level, msg string, fields ...logging.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := append([]logging.Field{}, fields...)
	l.entries = append(l.entries, logEntry{level: level, msg: msg, fields: cp})
}

func (l *captureLogger) last() (logEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 {
		return logEntry{}, false
	}
	return l.entries[len(l.entries)-1], true
}

func findField(fields []logging.Field, key string) (any, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func TestAccessLog_RedactsSensitiveHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("X-API-Key", "key-123")
	req.Header.Set("X-Custom", "hello")

	rec := httptest.NewRecorder()
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	logger := &captureLogger{}
	mw := AccessLog(AccessLogConfig{
		Logger:     logger,
		HeaderKeys: []string{"Authorization", "Cookie", "X-API-Key", "X-Custom"},
	})

	if err := mw(ctx, func() error { return ctx.String(200, "ok") }); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	entry, ok := logger.last()
	if !ok {
		t.Fatalf("expected log entry")
	}

	if v, ok := findField(entry.fields, "req_header_authorization"); !ok || v != "[REDACTED]" {
		t.Fatalf("expected authorization redacted, got %v (ok=%v)", v, ok)
	}
	if v, ok := findField(entry.fields, "req_header_cookie"); !ok || v != "[REDACTED]" {
		t.Fatalf("expected cookie redacted, got %v (ok=%v)", v, ok)
	}
	if v, ok := findField(entry.fields, "req_header_x_api_key"); !ok || v != "[REDACTED]" {
		t.Fatalf("expected x-api-key redacted, got %v (ok=%v)", v, ok)
	}
	if v, ok := findField(entry.fields, "req_header_x_custom"); !ok || v != "hello" {
		t.Fatalf("expected x-custom preserved, got %v (ok=%v)", v, ok)
	}
}
