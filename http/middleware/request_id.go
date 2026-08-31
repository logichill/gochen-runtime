package middleware

import (
	"strings"

	"gochen/contextx"
	"gochen/gen/uuid"
	"gochen/httpx"
)

// RequestIDConfig 定义请求ID配置。
type RequestIDConfig struct {
	// Header 读取/写入请求 ID 的头（默认 "X-Request-ID"）。
	Header string
}

// RequestID 注入 request_id，并回写到响应头。
func RequestID(cfg RequestIDConfig) httpx.Middleware {
	header := strings.TrimSpace(cfg.Header)
	if header == "" {
		header = "X-Request-ID"
	}

	return func(ctx httpx.IContext, next func() error) error {
		requestID := httpx.SanitizeIdentifierFromHeader(ctx.Header(header), 128)
		if requestID == "" {
			requestID = uuid.NewString()
		}

		reqCtx := ctx.RequestContext()
		derived, err := contextx.WithRequestID(reqCtx, requestID)
		if err != nil {
			return err
		}
		reqCtx = reqCtx.WithContext(derived)
		ctx.SetContext(reqCtx)
		ctx.SetHeader(header, requestID)

		return next()
	}
}
