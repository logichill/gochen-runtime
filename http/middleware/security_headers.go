package middleware

import "gochen/httpx"

// SecurityHeadersConfig 定义常用安全响应头配置。
type SecurityHeadersConfig struct {
	ContentTypeNoSniff    bool
	FrameOptions          string
	XSSProtection         string
	ReferrerPolicy        string
	ContentSecurityPolicy string
}

// SecureDefaults 返回一个“默认安全响应头套件”中间件。
//
// 说明：
// - 默认值偏保守：不默认启用 CSP/HSTS（避免对现有前端资源加载/多域集成造成破坏性影响）；
// - 如需自定义，请使用 SecurityHeaders(SecurityHeadersConfig)。
func SecureDefaults() httpx.Middleware {
	return SecurityHeaders(SecurityHeadersConfig{
		ContentTypeNoSniff: true,
		FrameOptions:       "DENY",
		// 现代浏览器已弃用该头；设置为 0 可避免旧版行为带来的误报/旁路。
		XSSProtection:  "0",
		ReferrerPolicy: "strict-origin-when-cross-origin",
	})
}

// SecurityHeaders 写入常用安全响应头。
//
// 说明：该中间件不做环境判断；调用方可按环境选择是否启用/如何配置 CSP。
func SecurityHeaders(cfg SecurityHeadersConfig) httpx.Middleware {
	return func(ctx httpx.IContext, next func() error) error {
		if cfg.ContentTypeNoSniff {
			ctx.SetHeader("X-Content-Type-Options", "nosniff")
		}
		if cfg.FrameOptions != "" {
			ctx.SetHeader("X-Frame-Options", cfg.FrameOptions)
		}
		if cfg.XSSProtection != "" {
			ctx.SetHeader("X-XSS-Protection", cfg.XSSProtection)
		}
		if cfg.ReferrerPolicy != "" {
			ctx.SetHeader("Referrer-Policy", cfg.ReferrerPolicy)
		}
		if cfg.ContentSecurityPolicy != "" {
			ctx.SetHeader("Content-Security-Policy", cfg.ContentSecurityPolicy)
		}
		return next()
	}
}
