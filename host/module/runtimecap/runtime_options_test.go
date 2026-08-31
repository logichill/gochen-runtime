package runtimecap

import (
	"testing"

	"gochen/httpx"
)

type testRouteGroup struct {
	prefixes       []string
	useArgCounts   []int
	lastMiddleware []httpx.Middleware
}

func (g *testRouteGroup) GET(string, httpx.Handler) httpx.IRouteGroup     { return g }
func (g *testRouteGroup) POST(string, httpx.Handler) httpx.IRouteGroup    { return g }
func (g *testRouteGroup) PUT(string, httpx.Handler) httpx.IRouteGroup     { return g }
func (g *testRouteGroup) DELETE(string, httpx.Handler) httpx.IRouteGroup  { return g }
func (g *testRouteGroup) PATCH(string, httpx.Handler) httpx.IRouteGroup   { return g }
func (g *testRouteGroup) HEAD(string, httpx.Handler) httpx.IRouteGroup    { return g }
func (g *testRouteGroup) OPTIONS(string, httpx.Handler) httpx.IRouteGroup { return g }

func (g *testRouteGroup) Group(prefix string) httpx.IRouteGroup {
	g.prefixes = append(g.prefixes, prefix)
	return g
}

func (g *testRouteGroup) Use(middlewares ...httpx.Middleware) httpx.IRouteGroup {
	g.useArgCounts = append(g.useArgCounts, len(middlewares))
	g.lastMiddleware = append([]httpx.Middleware(nil), middlewares...)
	return g
}

func TestModuleHTTPOptions_IsImmutableAfterConstruction(t *testing.T) {
	group := &testRouteGroup{}
	first := func(httpx.IContext, func() error) error { return nil }
	second := func(httpx.IContext, func() error) error { return nil }
	middlewares := []httpx.Middleware{first}

	opts := NewModuleHTTPOptions(group, "/iam", middlewares...)
	middlewares[0] = second

	if got := opts.Prefix(); got != "/iam" {
		t.Fatalf("expected prefix /iam, got %q", got)
	}
	if got := opts.Middlewares(); len(got) != 1 {
		t.Fatalf("expected one middleware, got %d", len(got))
	} else {
		got[0] = second
	}

	mounted := opts.MountGroup()
	if mounted != group {
		t.Fatalf("expected mount group to return child group")
	}
	if len(group.prefixes) != 1 || group.prefixes[0] != "/iam" {
		t.Fatalf("unexpected prefixes: %v", group.prefixes)
	}
	if len(group.useArgCounts) != 1 || group.useArgCounts[0] != 1 {
		t.Fatalf("unexpected middleware use counts: %v", group.useArgCounts)
	}
	if len(group.lastMiddleware) != 1 {
		t.Fatalf("expected copied middleware")
	}
}
