package quick_test

import (
	"context"
	"errors"
	"testing"

	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	gochenerrors "gochen/errors"
	"gochen/httpx"
	"gochen/testkit/assert"
	"gochen/testkit/require"
)

type mockRouteGroup struct {
	routes      []string
	middlewares []httpx.Middleware
}

func (m *mockRouteGroup) Group(prefix string) httpx.IRouteGroup {
	return m
}

func (m *mockRouteGroup) Use(middlewares ...httpx.Middleware) httpx.IRouteGroup {
	m.middlewares = append(m.middlewares, middlewares...)
	return m
}

func (m *mockRouteGroup) GET(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "GET "+path)
	return m
}

func (m *mockRouteGroup) POST(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "POST "+path)
	return m
}

func (m *mockRouteGroup) PUT(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "PUT "+path)
	return m
}

func (m *mockRouteGroup) DELETE(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "DELETE "+path)
	return m
}

func (m *mockRouteGroup) PATCH(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "PATCH "+path)
	return m
}

func (m *mockRouteGroup) HEAD(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "HEAD "+path)
	return m
}

func (m *mockRouteGroup) OPTIONS(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.routes = append(m.routes, "OPTIONS "+path)
	return m
}

type mockHTTPOptions struct {
	group httpx.IRouteGroup
}

func (m *mockHTTPOptions) MountGroup() httpx.IRouteGroup {
	return m.group
}

type testRouteRegistrarStruct struct {
	called bool
}

func (r *testRouteRegistrarStruct) RegisterRoutes(g httpx.IRouteGroup) error {
	r.called = true
	g.GET("/struct", func(c httpx.IContext) error { return nil })
	return nil
}

type customNamedRegistrar func(httpx.IRouteGroup) error

// otherRouteGroup 同样实现 httpx.IRouteGroup，但与宿主实际传入的
// *mockRouteGroup 是不同的具体类型——这正是 reflect 兜底放行具体类型形参时
// fnVal.Call 会 panic 的场景。
type otherRouteGroup struct{ mockRouteGroup }

// valueError 是值类型实现的 error：reflect 兜底若按 AssignableTo 放行它，
// res[0].IsNil() 会对非 nilable 类型 panic。
type valueError struct{}

func (valueError) Error() string { return "value error" }

func TestModuleQuick_BasicLifecycle(t *testing.T) {
	dummyMiddleware := func(c httpx.IContext, next func() error) error { return next() }
	ctor := quick.Module("test-mod", nil,
		quick.WithDependencies("dep1", "dep2"),
		quick.WithExtensions("ext1", 42),
		quick.WithMiddlewares(dummyMiddleware),
	)

	mod, err := ctor()
	require.NoError(t, err)
	assert.Equal(t, "test-mod", mod.ID())
	assert.Equal(t, "test-mod", mod.Name())

	depProvider, ok := mod.(module.IModuleDependencyProvider)
	require.True(t, ok)
	assert.Equal(t, []string{"dep1", "dep2"}, depProvider.DependsOn())

	extProvider, ok := mod.(module.IModuleExtensionProvider)
	require.True(t, ok)
	assert.Equal(t, []any{"ext1", 42}, extProvider.ModuleExtensions())

	stop, err := mod.Start(context.Background())
	require.NoError(t, err)
	assert.Nil(t, stop)

	// Invalid empty name
	_, err = quick.Module("  ", nil)()
	require.Error(t, err)
	assert.True(t, gochenerrors.Is(err, gochenerrors.InvalidInput))
}

func TestModuleQuick_RegisterRoutes_Branches(t *testing.T) {
	ctx := context.Background()

	// 1. ctx == nil
	t.Run("nil ctx fails", func(t *testing.T) {
		mod, err := quick.Module("test", func(g httpx.IRouteGroup) error { return nil })()
		require.NoError(t, err)
		rm, ok := mod.(module.IRouteModule)
		require.True(t, ok)
		err = rm.RegisterRoutes(nil)
		require.Error(t, err)
		assert.True(t, gochenerrors.Is(err, gochenerrors.InvalidInput))
	})

	// 2. registrar == nil
	t.Run("nil registrar succeeds gracefully", func(t *testing.T) {
		mod, err := quick.Module("test", nil)()
		require.NoError(t, err)
		rm := mod.(module.IRouteModule)
		err = rm.RegisterRoutes(ctx)
		require.NoError(t, err)
	})

	// 3. No HTTP capability on host
	t.Run("missing HTTP capability skips gracefully", func(t *testing.T) {
		called := false
		mod, err := quick.Module("test", func(g httpx.IRouteGroup) error {
			called = true
			return nil
		})()
		require.NoError(t, err)
		rm := mod.(module.IRouteModule)
		_ = rm.Init(module.ModuleInitOptions{}) // no HTTP capability
		err = rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.False(t, called)
	})

	// 4. MountGroup == nil
	t.Run("nil MountGroup skips gracefully", func(t *testing.T) {
		called := false
		mod, err := quick.Module("test", func(g httpx.IRouteGroup) error {
			called = true
			return nil
		})()
		require.NoError(t, err)
		rm := mod.(module.IRouteModule)
		opts := module.WithCapability[module.IHTTPOptions](module.ModuleInitOptions{}, module.HTTPKey, &mockHTTPOptions{group: nil})
		_ = rm.Init(opts)
		err = rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.False(t, called)
	})

	// Helper for initialized route modules
	initRouteModule := func(t *testing.T, registrar any, opts ...quick.ModuleOption) (module.IRouteModule, *mockRouteGroup) {
		mod, err := quick.Module("test-mod", registrar, opts...)()
		require.NoError(t, err)
		rm := mod.(module.IRouteModule)
		group := &mockRouteGroup{}
		initOpts := module.WithCapability[module.IHTTPOptions](module.ModuleInitOptions{}, module.HTTPKey, &mockHTTPOptions{group: group})
		err = rm.Init(initOpts)
		require.NoError(t, err)
		return rm, group
	}

	// 5. Branch: func(httpx.IRouteGroup) error
	t.Run("branch func(httpx.IRouteGroup) error", func(t *testing.T) {
		called := false
		rm, group := initRouteModule(t, func(g httpx.IRouteGroup) error {
			called = true
			g.GET("/fn", func(c httpx.IContext) error { return nil })
			return nil
		})
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.True(t, called)
		assert.Contains(t, group.routes, "GET /fn")
	})

	// 6. Branch: interface{ RegisterRoutes(httpx.IRouteGroup) error }
	t.Run("branch interface with RegisterRoutes", func(t *testing.T) {
		regStruct := &testRouteRegistrarStruct{}
		rm, group := initRouteModule(t, regStruct)
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.True(t, regStruct.called)
		assert.Contains(t, group.routes, "GET /struct")
	})

	// 7. Branch: func() (interface{ RegisterRoutes(httpx.IRouteGroup) error }, error)
	t.Run("branch factory returning interface", func(t *testing.T) {
		regStruct := &testRouteRegistrarStruct{}
		factory := func() (interface{ RegisterRoutes(httpx.IRouteGroup) error }, error) {
			return regStruct, nil
		}
		rm, group := initRouteModule(t, factory)
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.True(t, regStruct.called)
		assert.Contains(t, group.routes, "GET /struct")
	})

	// 8. Branch: func() (func(httpx.IRouteGroup) error, error)
	t.Run("branch factory returning func", func(t *testing.T) {
		called := false
		factory := func() (func(httpx.IRouteGroup) error, error) {
			return func(g httpx.IRouteGroup) error {
				called = true
				g.POST("/factory-fn", func(c httpx.IContext) error { return nil })
				return nil
			}, nil
		}
		rm, group := initRouteModule(t, factory)
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.True(t, called)
		assert.Contains(t, group.routes, "POST /factory-fn")
	})

	// 9. Branch: Named func type (type CustomNamedRegistrar func(httpx.IRouteGroup) error)
	t.Run("branch named func type via reflect fallback", func(t *testing.T) {
		called := false
		namedReg := customNamedRegistrar(func(g httpx.IRouteGroup) error {
			called = true
			g.DELETE("/named", func(c httpx.IContext) error { return nil })
			return nil
		})
		rm, group := initRouteModule(t, namedReg)
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.True(t, called)
		assert.Contains(t, group.routes, "DELETE /named")
	})

	// 10. Branch: Unsupported type (default branch)
	t.Run("branch unsupported registrar type returns InvalidInput", func(t *testing.T) {
		rm, _ := initRouteModule(t, 12345)
		err := rm.RegisterRoutes(ctx)
		require.Error(t, err)
		assert.True(t, gochenerrors.Is(err, gochenerrors.InvalidInput))
	})

	// 10a. reflect 兜底不得放行具体类型形参：实际传入的是宿主给的另一个具体
	// 类型，放行会在 fnVal.Call 处 panic，必须落到 default 报 InvalidInput。
	t.Run("concrete param registrar returns InvalidInput instead of panicking", func(t *testing.T) {
		rm, _ := initRouteModule(t, func(g *otherRouteGroup) error { return nil })
		err := rm.RegisterRoutes(ctx)
		require.Error(t, err)
		assert.True(t, gochenerrors.Is(err, gochenerrors.InvalidInput))
	})

	// 10b. reflect 兜底不得放行非 error 返回值：值类型实现 error 时
	// res[0].IsNil() 会 panic，必须落到 default 报 InvalidInput。
	t.Run("non-error return registrar returns InvalidInput instead of panicking", func(t *testing.T) {
		rm, _ := initRouteModule(t, func(g httpx.IRouteGroup) valueError { return valueError{} })
		err := rm.RegisterRoutes(ctx)
		require.Error(t, err)
		assert.True(t, gochenerrors.Is(err, gochenerrors.InvalidInput))
	})

	// 11. Branch: Factory error propagation
	t.Run("factory returning error propagates", func(t *testing.T) {
		sentinel := errors.New("factory init failed")
		factory := func() (func(httpx.IRouteGroup) error, error) {
			return nil, sentinel
		}
		rm, _ := initRouteModule(t, factory)
		err := rm.RegisterRoutes(ctx)
		require.Error(t, err)
		assert.True(t, errors.Is(err, sentinel))
	})

	// 12. Branch: Registrar error propagation
	t.Run("registrar execution error propagates", func(t *testing.T) {
		sentinel := errors.New("route registration conflict")
		rm, _ := initRouteModule(t, func(g httpx.IRouteGroup) error {
			return sentinel
		})
		err := rm.RegisterRoutes(ctx)
		require.Error(t, err)
		assert.True(t, errors.Is(err, sentinel))
	})

	// 13. Middlewares attached to module
	t.Run("module middlewares applied to group", func(t *testing.T) {
		dummyMw := func(c httpx.IContext, next func() error) error { return next() }
		rm, group := initRouteModule(t, func(g httpx.IRouteGroup) error { return nil }, quick.WithMiddlewares(dummyMw))
		err := rm.RegisterRoutes(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, len(group.middlewares))
	})
}
