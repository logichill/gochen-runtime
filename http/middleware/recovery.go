package middleware

import (
	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
	"runtime/debug"
)

// RecoveryConfig 配置 panic recovery 中间件的可观测性行为。
type RecoveryConfig struct {
	// Logger 用于输出 panic recovered 日志；nil 表示使用默认 StdLogger 兜底。
	Logger logging.ILogger
}

func Recovery() httpx.Middleware {
	return RecoveryWithConfig(RecoveryConfig{})
}

func RecoveryWithConfig(cfg RecoveryConfig) httpx.Middleware {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.ComponentLogger("gochen.http.recovery")
	}

	return func(ctx httpx.IContext, next func() error) (err error) {
		defer func() {
			if r := recover(); r != nil {
				base := contextx.Background()
				path := ""
				method := ""
				if ctx != nil {
					if c := ctx.RequestContext(); c != nil {
						base = c
					}
					path = ctx.Path()
					method = ctx.Method()
				}
				if contextx.TraceID(base) == "" {
					if c, werr := contextx.WithTraceID(base, contextx.GenerateTraceID()); werr == nil && c != nil {
						base = c
					}
				}
				logger.Error(base, "panic_recovered",
					logging.Any("panic", r),
					logging.String("stack", string(debug.Stack())),
					logging.String("path", path),
					logging.String("method", method),
				)
				err = errors.NewCode(errors.Internal, "internal server error")
			}
		}()
		return next()
	}
}
