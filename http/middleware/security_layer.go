package middleware

import (
	"gochen/contextx"
	"gochen/httpx"
)

// SecurityLayerConfig 定义安全Layer配置。
type SecurityLayerConfig struct {
	Layer        httpx.SecurityLayer
	AllowSession bool
}

func AsAPI() httpx.Middleware {
	return SecurityLayer(SecurityLayerConfig{
		Layer:        httpx.SecurityLayerAPI,
		AllowSession: false,
	})
}

func AsAPIAllowSession() httpx.Middleware {
	return SecurityLayer(SecurityLayerConfig{
		Layer:        httpx.SecurityLayerAPI,
		AllowSession: true,
	})
}

func AsWeb() httpx.Middleware {
	return SecurityLayer(SecurityLayerConfig{
		Layer:        httpx.SecurityLayerWeb,
		AllowSession: true,
	})
}

func SecurityLayer(cfg SecurityLayerConfig) httpx.Middleware {
	return func(ctx httpx.IContext, next func() error) error {
		if ctx == nil {
			return next()
		}
		rc := ctx.RequestContext()
		if rc == nil {
			return next()
		}
		layer := cfg.Layer
		if layer == "" {
			return next()
		}

		derived, err := contextx.WithSessionVisibility(rc, cfg.AllowSession)
		if err != nil {
			return err
		}
		rc = httpx.WithSecurityLayer(rc.WithContext(derived), layer)
		ctx.SetContext(rc)
		return next()
	}
}
