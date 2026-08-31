package middleware

import (
	"gochen/contextx"
	"gochen/httpx"
	"strings"
)

// TraceIDConfig 定义追踪ID配置。
type TraceIDConfig struct {
	// Header 读取/写入 trace_id 的头（默认 "X-Trace-ID"）。
	Header string
}

// TraceID 注入 trace_id，并回写到响应头。
//
// 注意：
// - 这里的 trace_id 是“业务关联 ID / correlation id”，用于日志与错误响应关联；
// - 它不同于 W3C Trace Context 的 `traceparent`（分布式追踪/OTEL TraceID）。
func TraceID(cfg TraceIDConfig) httpx.Middleware {
	header := strings.TrimSpace(cfg.Header)
	if header == "" {
		header = "X-Trace-ID"
	}

	return func(ctx httpx.IContext, next func() error) error {
		traceID := httpx.SanitizeIdentifierFromHeader(ctx.Header(header), 128)
		if traceID == "" {
			traceID = contextx.GenerateTraceID()
		}

		reqCtx := ctx.RequestContext()
		derived, err := contextx.WithTraceID(reqCtx, traceID)
		if err != nil {
			return err
		}
		reqCtx = reqCtx.WithContext(derived)
		ctx.SetContext(reqCtx)
		ctx.SetHeader(header, traceID)

		return next()
	}
}
