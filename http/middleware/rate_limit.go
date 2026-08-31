package middleware

import (
	"strings"

	"gochen/errors"
	"gochen/httpx"
	"gochen/policy/ratelimit"
)

// RateLimitConfig 限流配置。
type RateLimitConfig struct {
	ratelimit.Config

	// KeyFn 生成限流 key（默认使用 ctx.ClientIP()）。
	KeyFn func(ctx httpx.IContext) string

	// SkipPaths 不限流的路径（精确匹配）。
	SkipPaths []string
}

// RateLimit 限流中间件（按 key 维度 token bucket）。
func RateLimit(cfg RateLimitConfig) httpx.Middleware {
	keyFn := cfg.KeyFn
	if keyFn == nil {
		keyFn = func(ctx httpx.IContext) string { return ctx.ClientIP() }
	}

	limiter := ratelimit.New(cfg.Config)

	filter := newPathFilter(cfg.SkipPaths, nil)

	return func(ctx httpx.IContext, next func() error) error {
		if filter.matches(ctx.Path()) {
			return next()
		}

		key := strings.TrimSpace(keyFn(ctx))
		if key == "" {
			key = "anonymous:" + ctx.ClientIP()
		}

		if !limiter.Allow(key) {
			return errors.NewCode(errors.TooManyRequests, "too many requests")
		}

		return next()
	}
}
