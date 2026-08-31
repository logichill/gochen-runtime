package middleware

import (
	"strings"
	"time"

	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
)

// AccessLogConfig 访问日志中间件配置。
type AccessLogConfig struct {
	// Logger 用于输出访问日志；默认使用 component=gochen.http.access_log 的全局 logger。
	Logger logging.ILogger

	// SkipPaths 不输出访问日志的路径（精确匹配）。
	SkipPaths []string

	// SkipPathPrefixes 不输出访问日志的路径前缀（前缀匹配）。
	SkipPathPrefixes []string

	// HeaderKeys 选择性输出的请求头名称列表（例如 "User-Agent"、"X-Forwarded-For"）。
	// 注意：Authorization/Cookie/X-API-Key/Set-Cookie 等敏感头会被自动脱敏。
	HeaderKeys []string

	// SlowThreshold 当耗时超过该阈值时，将日志级别提升为 warn（仅对成功请求生效）。
	SlowThreshold time.Duration
}

type bytesWriter interface {
	BytesWritten() int64
}

// AccessLog 输出请求访问日志（method/path/status/duration/trace 等）。
//
// 说明：
// - 该中间件不负责写响应；返回的 error 仍由底层 http server 统一处理；
// - 为避免泄露敏感信息，仅在显式指定 HeaderKeys 时才输出请求头，并对敏感头做脱敏。
func AccessLog(cfg AccessLogConfig) httpx.Middleware {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.ComponentLogger("gochen.http.access_log")
	}

	filter := newPathFilter(cfg.SkipPaths, cfg.SkipPathPrefixes)

	headerKeys := make([]string, 0, len(cfg.HeaderKeys))
	for _, h := range cfg.HeaderKeys {
		h = strings.TrimSpace(h)
		if h != "" {
			headerKeys = append(headerKeys, h)
		}
	}

	slowThreshold := cfg.SlowThreshold
	if slowThreshold < 0 {
		slowThreshold = 0
	}

	return func(ctx httpx.IContext, next func() error) error {
		if ctx == nil {
			return next()
		}

		path := ctx.Path()
		if filter.matches(path) {
			return next()
		}

		start := time.Now()
		err := next()
		elapsed := time.Since(start)

		status := responseStatus(ctx, err)

		fields := make([]logging.Field, 0, 16+len(headerKeys))
		fields = append(fields,
			logging.String("method", ctx.Method()),
			logging.String("path", path),
			logging.Int("status", status),
			logging.Int64("duration_ms", elapsed.Milliseconds()),
			logging.String("client_ip", ctx.ClientIP()),
		)

		if ua := ctx.UserAgent(); ua != "" {
			fields = append(fields, logging.String("user_agent", ua))
		}

		if reqCtx := ctx.RequestContext(); reqCtx != nil {
			if v := contextx.TraceID(reqCtx); v != "" {
				fields = append(fields, logging.String("trace_id", v))
			}
			if v := contextx.RequestID(reqCtx); v != "" {
				fields = append(fields, logging.String("request_id", v))
			}
			if v := contextx.TenantID(reqCtx); v != "" {
				fields = append(fields, logging.String("tenant_id", v))
			}
			if v := contextx.UserID(reqCtx); v != 0 {
				fields = append(fields, logging.Int64("user_id", v))
			}
		}

		if bw, ok := ctx.(bytesWriter); ok {
			if n := bw.BytesWritten(); n > 0 {
				fields = append(fields, logging.Int64("bytes", n))
			}
		}

		for _, h := range headerKeys {
			v := ctx.Header(h)
			if v == "" {
				continue
			}
			if isSensitiveHeader(h) {
				v = "[REDACTED]"
			}
			fields = append(fields, logging.String(normalizeHeaderFieldKey(h), v))
		}

		if err != nil {
			logger.Error(ctx.RequestContext(), "http_request",
				append(fields, logging.String("error_code", string(errors.Code(err))), logging.Error(err))...,
			)
			return err
		}

		// 成功请求：根据状态码/耗时选择日志级别。
		switch {
		case status >= 500:
			logger.Error(ctx.RequestContext(), "http_request", fields...)
		case status >= 400:
			logger.Warn(ctx.RequestContext(), "http_request", fields...)
		case slowThreshold > 0 && elapsed >= slowThreshold:
			logger.Warn(ctx.RequestContext(), "http_request", append(fields, logging.Bool("slow", true))...)
		default:
			logger.Info(ctx.RequestContext(), "http_request", fields...)
		}

		return nil
	}
}

// isSensitiveHeader 判断Sensitive请求头。
func isSensitiveHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "cookie", "set-cookie", "x-api-key":
		return true
	default:
		return false
	}
}

// normalizeHeaderFieldKey 规范化请求头字段Key。
func normalizeHeaderFieldKey(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "-", "_")
	return "req_header_" + n
}
