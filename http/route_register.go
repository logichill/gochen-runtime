package http

import (
	nethttp "net/http"
	"strings"

	"gochen/errors"
	"gochen/httpx"
)

// RegisterRoute 把一条路由按 method 字符串注册到 group 上。
//
// httpx.IRouteGroup 为每个 HTTP 方法提供一个同名方法，但调用方通常只持有
// "GET"/"POST" 这样的字符串。把 method→方法 的分派集中在这里，避免各处各自维护
// 一份相同的 switch，也避免旧实现里"未知 method 静默注册成 OPTIONS"的行为。
//
// method 不区分大小写；不在 net/http 标准方法集合内时返回 InvalidInput。
// path 允许为空：IRouteGroup 把空路径解释为"组根路径"。
func RegisterRoute(group httpx.IRouteGroup, method, path string, handler httpx.Handler) error {
	if group == nil {
		return errors.NewCode(errors.InvalidInput, "route group is nil")
	}
	if handler == nil {
		return errors.NewCode(errors.InvalidInput, "route handler cannot be nil")
	}
	path = strings.TrimSpace(path)
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case nethttp.MethodGet:
		group.GET(path, handler)
	case nethttp.MethodPost:
		group.POST(path, handler)
	case nethttp.MethodPut:
		group.PUT(path, handler)
	case nethttp.MethodDelete:
		group.DELETE(path, handler)
	case nethttp.MethodPatch:
		group.PATCH(path, handler)
	case nethttp.MethodHead:
		group.HEAD(path, handler)
	case nethttp.MethodOptions:
		group.OPTIONS(path, handler)
	default:
		return errors.NewCode(errors.InvalidInput, "unsupported route method: "+method).
			WithContext("method", method)
	}
	return nil
}
