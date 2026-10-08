package monitoringhttp

import (
	"net/http"
	"testing"

	"gochen/testkit/require"
)

func TestRoutesDefaultRouteSetIncludesHealthOnly(t *testing.T) {
	routes := Routes(DefaultRouteSet())

	require.Equal(t, []RouteName{RouteHealthz, RouteReadyz}, routeNames(routes))
	require.Equal(t, []string{http.MethodGet, http.MethodGet}, routeMethods(routes))
}

func TestRoutesFullRouteSetIncludesHealthAndMonitoring(t *testing.T) {
	routes := Routes(FullRouteSet())

	require.Equal(t, []RouteName{RouteHealthz, RouteReadyz, RouteMetrics, RouteSnapshot}, routeNames(routes))
	require.Equal(t, []string{http.MethodGet, http.MethodGet, http.MethodGet, http.MethodGet}, routeMethods(routes))
}

func TestRoutesHostRouteSetFiltersByFlags(t *testing.T) {
	healthOnly := Routes(HostRouteSet(true, false))
	require.Equal(t, []RouteName{RouteHealthz, RouteReadyz}, routeNames(healthOnly))

	monitoringOnly := Routes(HostRouteSet(false, true))
	require.Equal(t, []RouteName{RouteMetrics, RouteSnapshot}, routeNames(monitoringOnly))

	require.Empty(t, Routes(HostRouteSet(false, false)))
}

func routeNames(routes []Route) []RouteName {
	names := make([]RouteName, 0, len(routes))
	for _, route := range routes {
		names = append(names, route.Name)
	}
	return names
}

func routeMethods(routes []Route) []string {
	methods := make([]string, 0, len(routes))
	for _, route := range routes {
		methods = append(methods, route.Method)
	}
	return methods
}
