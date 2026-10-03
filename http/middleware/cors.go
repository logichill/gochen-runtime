package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/nethttp"
	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
)

// CORSConfig CORS 配置。
type CORSConfig struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           int
}

const DefaultCORSMaxAge = 86400

func DefaultCORSAllowMethods() []string {
	return []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
}

func DefaultCORSAllowHeaders() []string {
	return []string{"Content-Type", "Authorization", "Origin", "X-CSRF-Token"}
}

// ValidateCORSConfig 校验 CORS 配置是否可安全启用。
func ValidateCORSConfig(cfg *CORSConfig) error {
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		return nil
	}
	if cfg.AllowCredentials && contains(cfg.AllowOrigins, "*") {
		return errors.NewCode(errors.InvalidInput, "CORS allow credentials cannot be used with wildcard origin")
	}
	return nil
}

func CORSFromWebConfig(cfg *runtimehttp.WebConfig) httpx.Middleware {
	logger := logging.ComponentLogger("gochen.http.cors")
	// 安全默认：nil config 等价于未启用 CORS。
	if cfg == nil {
		return func(ctx httpx.IContext, next func() error) error { return next() }
	}
	if !cfg.CORSEnabled {
		logger.Warn(context.Background(), "CORS is disabled by default; set CORSEnabled=true and CORSAllowOrigins explicitly to enable cross-origin requests")
		return func(ctx httpx.IContext, next func() error) error { return next() }
	}
	// 显式 allowlist：启用 CORS 时必须显式设置允许的 origins。
	if len(cfg.CORSAllowOrigins) == 0 {
		logger.Warn(context.Background(), "CORS is enabled but CORSAllowOrigins is empty; cross-origin requests remain closed")
		return func(ctx httpx.IContext, next func() error) error { return next() }
	}
	methods := cfg.CORSAllowMethods
	if len(methods) == 0 {
		methods = DefaultCORSAllowMethods()
	}
	headers := cfg.CORSAllowHeaders
	if len(headers) == 0 {
		headers = DefaultCORSAllowHeaders()
	}
	allowCredentials := cfg.CORSAllowCredentials && !contains(cfg.CORSAllowOrigins, "*")
	corsCfg := &CORSConfig{
		AllowOrigins:  cfg.CORSAllowOrigins,
		AllowMethods:  methods,
		AllowHeaders:  headers,
		ExposeHeaders: nil,
		// credentials 仅在显式开启且非 * origin 时启用。
		AllowCredentials: allowCredentials,
		MaxAge:           DefaultCORSMaxAge,
	}
	if err := ValidateCORSConfig(corsCfg); err != nil {
		return func(ctx httpx.IContext, next func() error) error { return err }
	}
	return CORS(corsCfg)
}

// CORS CORS 中间件。
func CORS(cfg *CORSConfig) httpx.Middleware {
	// 安全默认：必须显式传入 allowlist，否则视为未启用。
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		return func(ctx httpx.IContext, next func() error) error { return next() }
	}
	if err := ValidateCORSConfig(cfg); err != nil {
		return func(ctx httpx.IContext, next func() error) error { return err }
	}
	c := cfg
	allowAllOrigins := contains(c.AllowOrigins, "*")
	allowedOrigins := makeStringSet(c.AllowOrigins)
	allowMethods := strings.Join(c.AllowMethods, ", ")
	allowHeaders := strings.Join(c.AllowHeaders, ", ")
	exposeHeaders := strings.Join(c.ExposeHeaders, ", ")
	maxAge := strconv.Itoa(c.MaxAge)

	return func(ctx httpx.IContext, next func() error) error {
		if !allowAllOrigins {
			varyByOrigin(ctx)
		}
		origin := ctx.Header("Origin")
		// 仅将满足 W3C CORS 预检条件的 OPTIONS 请求视为 preflight：
		// - method=OPTIONS
		// - 存在 Origin
		// - 存在 Access-Control-Request-Method
		//
		// 这样可以避免把“业务自定义 OPTIONS 端点”误判为 preflight 而被短路。
		isPreflight := ctx.Method() == http.MethodOptions && origin != "" && ctx.Header("Access-Control-Request-Method") != ""
		corsAllowed := false

		if origin != "" && len(c.AllowOrigins) > 0 {
			if allowAllOrigins {
				ctx.SetHeader("Access-Control-Allow-Origin", "*")
				corsAllowed = true
			} else {
				if _, ok := allowedOrigins[origin]; ok {
					ctx.SetHeader("Access-Control-Allow-Origin", origin)
					corsAllowed = true
				}
			}
		}

		if corsAllowed && allowMethods != "" {
			ctx.SetHeader("Access-Control-Allow-Methods", allowMethods)
		}
		if corsAllowed && allowHeaders != "" {
			ctx.SetHeader("Access-Control-Allow-Headers", allowHeaders)
		}
		if corsAllowed && exposeHeaders != "" {
			ctx.SetHeader("Access-Control-Expose-Headers", exposeHeaders)
		}
		if corsAllowed && c.AllowCredentials && !allowAllOrigins {
			ctx.SetHeader("Access-Control-Allow-Credentials", "true")
		}
		if corsAllowed && c.MaxAge > 0 {
			ctx.SetHeader("Access-Control-Max-Age", maxAge)
		}

		if isPreflight {
			return ctx.String(http.StatusNoContent, "")
		}

		return next()
	}
}

// allowlist 响应始终随 Origin 变化，包括未携带 Origin 的请求。
func varyByOrigin(ctx httpx.IContext) {
	if writer, ok := nethttp.ResponseWriterOf(ctx); ok && writer != nil {
		header := writer.Header()
		for _, value := range header.Values("Vary") {
			for _, name := range strings.Split(value, ",") {
				name = strings.TrimSpace(name)
				if name == "*" || strings.EqualFold(name, "Origin") {
					return
				}
			}
		}
		header.Add("Vary", "Origin")
		return
	}
	ctx.SetHeader("Vary", "Origin")
}

func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

func makeStringSet(list []string) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, item := range list {
		out[item] = struct{}{}
	}
	return out
}
