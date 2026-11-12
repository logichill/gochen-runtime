package rest

import (
	"net/http"

	"gochen-runtime/http/middleware"
	"gochen-runtime/internal/httpheader"
	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

func registerPreflightRoutes(group httpx.IRouteGroup, mounted mountedRoutes, cors httpx.Middleware, registry IEntryRegistry, module string) error {
	if cors == nil {
		return nil
	}
	for path, methods := range mounted {
		if registry != nil {
			location, ok := group.(httpx.IRouteLocation)
			if !ok {
				return errors.NewCode(errors.InvalidInput, "route group must expose actual path resolution")
			}
			if err := registry.BindHTTP(http.MethodOptions, location.RoutePath(path), auth.EntryBinding{Module: module, ResourceAccess: auth.ResourceAccessNone, Source: "api/rest CORS preflight", AllowReason: "HTTP protocol negotiation"}); err != nil {
				return err
			}
		}
		allowHeader := httpheader.BuildAllowHeader(methods)
		group.OPTIONS(path, func(c httpx.IContext) error {
			if allowHeader != "" {
				c.SetHeader("Allow", allowHeader)
			}
			if isCORSPreflightRequest(c) {
				return cors(c, func() error {
					return c.String(http.StatusNoContent, "")
				})
			}
			return c.String(http.StatusNoContent, "")
		})
	}
	return nil
}

func newCORSMiddleware(cfg *middleware.CORSConfig) httpx.Middleware {
	if cfg == nil {
		return nil
	}
	origins := nonEmptyStrings(cfg.AllowOrigins)
	if len(origins) == 0 {
		return nil
	}
	methods := nonEmptyStrings(cfg.AllowMethods)
	if len(methods) == 0 {
		methods = middleware.DefaultCORSAllowMethods()
	}
	headers := nonEmptyStrings(cfg.AllowHeaders)
	if len(headers) == 0 {
		headers = middleware.DefaultCORSAllowHeaders()
	}
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = middleware.DefaultCORSMaxAge
	}
	return middleware.CORS(&middleware.CORSConfig{
		AllowOrigins:     origins,
		AllowMethods:     methods,
		AllowHeaders:     headers,
		ExposeHeaders:    nonEmptyStrings(cfg.ExposeHeaders),
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           maxAge,
	})
}
