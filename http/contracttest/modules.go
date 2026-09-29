package contracttest

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"

	"gochen-runtime/host"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	"gochen/httpx"
)

// mountedServer 只完成 Host 路由装配，不监听端口。
type mountedServer struct{ IServer }

func (s mountedServer) Start(string) error         { return nil }
func (s mountedServer) Stop(context.Context) error { return nil }

type firstRegistrar struct{ middleware httpx.Middleware }

func (r *firstRegistrar) RegisterRoutes(g httpx.IRouteGroup) error {
	g.Use(r.middleware)
	g.GET("/first", moduleResponse)
	return nil
}

type secondRegistrar struct{}

func (*secondRegistrar) RegisterRoutes(g httpx.IRouteGroup) error {
	g.GET("/second", moduleResponse)
	return nil
}
func moduleResponse(c httpx.IContext) error { return c.String(200, "ok") }

func runModules(t *testing.T, newServer func(*testing.T) IServer) {
	srv := newServer(t)
	var trace []string
	mw := func(name string) httpx.Middleware {
		return func(c httpx.IContext, next func() error) error {
			trace = append(trace, name)
			err := next()
			trace = append(trace, "/"+name)
			return err
		}
	}
	route := func(g httpx.IRouteGroup) error { g.GET("/ping", moduleResponse); return nil }
	app := quick.New(config.WithHTTPServer(mountedServer{srv}))
	group := app.Group("/staff")
	nested := group.Group("/v2").Use(mw("nested"))
	calls, starts, stops := 0, 0, 0
	nested.Modules(func() (module.IModule, error) {
		calls++
		return host.Module("orders").
			Middleware(mw("module")).
			RouteRegistrar(
				func() *firstRegistrar { return &firstRegistrar{mw("local")} },
				func() *secondRegistrar { return &secondRegistrar{} },
			).
			OnStart(func(context.Context) error { starts++; return nil }).
			OnStop(func(context.Context) error { stops++; return nil }).
			Build()
	})
	group.Mount("/", quick.Module("root", route))
	group.Mount("/custom", quick.Module("special", route))
	app.Modules(quick.Module("public", route))
	// 声明模块后追加的父级策略，在构建时统一继承。
	group.Use(mw("group"))
	app.Use(mw("global"))
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || starts != 1 || stops != 1 {
		t.Fatalf("lifecycle calls=%d starts=%d stops=%d", calls, starts, stops)
	}
	for _, tc := range []struct {
		path  string
		chain []string
	}{
		{"/api/v1/staff/v2/orders/first", []string{"global", "group", "nested", "module", "local"}},
		{"/api/v1/staff/v2/orders/second", []string{"global", "group", "nested", "module"}},
		{"/api/v1/staff/ping", []string{"global", "group"}},
		{"/api/v1/staff/custom/ping", []string{"global", "group"}},
		{"/api/v1/public/ping", []string{"global"}},
		{"/api/v1/healthz", []string{"global"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			trace = nil
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			want := append([]string(nil), tc.chain...)
			for i := len(tc.chain) - 1; i >= 0; i-- {
				want = append(want, "/"+tc.chain[i])
			}
			if rec.Code != 200 || !reflect.DeepEqual(trace, want) {
				t.Fatalf("status=%d middleware=%v want=%v", rec.Code, trace, want)
			}
		})
	}
}
