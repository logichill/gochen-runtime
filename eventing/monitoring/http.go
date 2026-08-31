// Package monitoringhttp 提供 eventing monitoring 的 net/http 适配器。
package monitoringhttp

import (
	"encoding/json"
	"net/http"

	"gochen/eventing/monitoring"
)

// NewHandler 暴露默认的 `/healthz` 和 `/readyz` 健康端点。
func NewHandler(reg *monitoring.Registry) http.Handler {
	return NewHandlerWithRoutes(reg, monitoring.DefaultRouteSet())
}

// NewHandlerWithRoutes 按指定路由集合构造 net/http Handler。
// metrics 与 snapshot 需要由组合根显式启用并施加鉴权或网络边界。
func NewHandlerWithRoutes(reg *monitoring.Registry, set monitoring.RouteSet) http.Handler {
	if reg == nil {
		reg = monitoring.DefaultRegistry()
	}

	mux := http.NewServeMux()
	for _, route := range monitoring.Routes(set) {
		route := route
		mux.HandleFunc(route.Path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != route.Method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			code, body := monitoring.HandleRoute(r.Context(), reg, route)
			writeJSON(w, code, body)
		})
	}
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
