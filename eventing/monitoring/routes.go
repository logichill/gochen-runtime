package monitoringhttp

import (
	"gochen/eventing/monitoring"

	"context"
)

const (
	methodGet                = "GET"
	statusOK                 = 200
	statusServiceUnavailable = 503
)

// RouteName 表示内置监控路由名称。
type RouteName string

const (
	RouteHealthz  RouteName = "healthz"
	RouteReadyz   RouteName = "readyz"
	RouteMetrics  RouteName = "metrics"
	RouteSnapshot RouteName = "snapshot"
)

// Route 描述一个内置监控端点。
type Route struct {
	Name    RouteName
	Method  string
	Path    string
	Handle  func(context.Context, *monitoring.Registry) (int, any)
	Enabled func(RouteSet) bool
}

// RouteSet 控制内置监控端点集合。
type RouteSet struct {
	Health     bool
	Monitoring bool
}

// DefaultRouteSet 返回独立 HTTP handler 默认暴露的安全路由集合。
func DefaultRouteSet() RouteSet {
	return RouteSet{Health: true}
}

// FullRouteSet 返回完整监控路由集合。
func FullRouteSet() RouteSet {
	return RouteSet{Health: true, Monitoring: true}
}

// HostRouteSet 返回 host 默认使用的路由集合。
func HostRouteSet(enableHealth, enableMonitoring bool) RouteSet {
	return RouteSet{Health: enableHealth, Monitoring: enableMonitoring}
}

// Routes 返回指定集合启用的内置监控路由。
func Routes(set RouteSet) []Route {
	routes := make([]Route, 0, len(allRoutes))
	for _, route := range allRoutes {
		if route.Enabled == nil || route.Enabled(set) {
			routes = append(routes, route)
		}
	}
	return routes
}

var allRoutes = []Route{
	{
		Name:   RouteHealthz,
		Method: methodGet,
		Path:   "/healthz",
		Handle: func(ctx context.Context, reg *monitoring.Registry) (int, any) {
			report := reg.Health.Report(ctx)
			return healthStatusCode(report.Status), report
		},
		Enabled: func(set RouteSet) bool { return set.Health },
	},
	{
		Name:   RouteReadyz,
		Method: methodGet,
		Path:   "/readyz",
		Handle: func(ctx context.Context, reg *monitoring.Registry) (int, any) {
			report := reg.Health.Report(ctx)
			return healthStatusCode(report.Status), report
		},
		Enabled: func(set RouteSet) bool { return set.Health },
	},
	{
		Name:   RouteMetrics,
		Method: methodGet,
		Path:   "/metrics",
		Handle: func(_ context.Context, reg *monitoring.Registry) (int, any) {
			return statusOK, reg.Metrics.Snapshot().Summary()
		},
		Enabled: func(set RouteSet) bool { return set.Monitoring },
	},
	{
		Name:   RouteSnapshot,
		Method: methodGet,
		Path:   "/snapshot",
		Handle: func(ctx context.Context, reg *monitoring.Registry) (int, any) {
			snapshot := reg.Snapshot(ctx)
			return healthStatusCode(snapshot.Health.Status), snapshot
		},
		Enabled: func(set RouteSet) bool { return set.Monitoring },
	},
}

func healthStatusCode(status monitoring.HealthStatus) int {
	if status == monitoring.HealthStatusUnhealthy {
		return statusServiceUnavailable
	}
	return statusOK
}
