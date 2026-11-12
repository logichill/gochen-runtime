package http

import (
	"reflect"

	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
)

// IEntryRegistry 登记 HTTP 执行绑定，驱动独立提供实际注册清单。
type IEntryRegistry interface {
	BindHTTP(method, path string, binding auth.EntryBinding) error
}

// IAuthorizationExecution 提供已装配执行入口的资源和条件执行绑定。
type IAuthorizationExecution interface {
	AuthorizationBinding(action string) (auth.EntryBinding, error)
}

type authorizationGroup struct {
	httpx.IRouteGroup
	location httpx.IRouteLocation
	registry IEntryRegistry
	module   string
}

// WithEntryRegistry 让嵌套路由组共享入口登记能力和模块来源。
func WithEntryRegistry(group httpx.IRouteGroup, registry IEntryRegistry, module string) (httpx.IRouteGroup, error) {
	if isNilEntryDependency(group) || isNilEntryDependency(registry) {
		return nil, errors.NewCode(errors.InvalidInput, "authorization route group requires a group and entry registry")
	}
	location, ok := group.(httpx.IRouteLocation)
	if !ok {
		return nil, errors.NewCode(errors.InvalidInput, "authorization route group requires actual path resolution")
	}
	return &authorizationGroup{IRouteGroup: group, location: location, registry: registry, module: module}, nil
}

// EntryRegistryFrom 返回路由组装配的入口注册表与模块来源。
func EntryRegistryFrom(group httpx.IRouteGroup) (IEntryRegistry, string, error) {
	if wrapped, ok := group.(*authorizationGroup); ok && (wrapped == nil || isNilEntryDependency(wrapped.location)) {
		return nil, "", errors.NewCode(errors.InvalidInput, "authorization route group requires actual path resolution")
	}
	provider, ok := group.(interface {
		AuthorizationEntries() (IEntryRegistry, string)
	})
	if !ok {
		return nil, "", nil
	}
	registry, module := provider.AuthorizationEntries()
	if isNilEntryDependency(registry) {
		return nil, "", errors.NewCode(errors.InvalidInput, "authorization entry registry cannot be nil")
	}
	return registry, module, nil
}

func (g *authorizationGroup) AuthorizationEntries() (IEntryRegistry, string) {
	return g.registry, g.module
}

func (g *authorizationGroup) RoutePath(relative string) string { return g.location.RoutePath(relative) }

func (g *authorizationGroup) Group(prefix string) httpx.IRouteGroup {
	group := g.IRouteGroup.Group(prefix)
	// 子组通过驱动提供与父组相同的路径能力；缺少能力时登记入口会明确失败。
	location, _ := group.(httpx.IRouteLocation)
	return &authorizationGroup{IRouteGroup: group, location: location, registry: g.registry, module: g.module}
}

func (g *authorizationGroup) Use(middlewares ...httpx.Middleware) httpx.IRouteGroup {
	g.IRouteGroup.Use(middlewares...)
	return g
}

func (g *authorizationGroup) GET(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.GET(path, handler)
	return g
}
func (g *authorizationGroup) POST(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.POST(path, handler)
	return g
}
func (g *authorizationGroup) PUT(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.PUT(path, handler)
	return g
}
func (g *authorizationGroup) DELETE(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.DELETE(path, handler)
	return g
}
func (g *authorizationGroup) PATCH(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.PATCH(path, handler)
	return g
}
func (g *authorizationGroup) HEAD(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.HEAD(path, handler)
	return g
}
func (g *authorizationGroup) OPTIONS(path string, handler httpx.Handler) httpx.IRouteGroup {
	g.IRouteGroup.OPTIONS(path, handler)
	return g
}

// BindEntry 采用实际驱动路径登记入口的已编译执行绑定。
func BindEntry(group httpx.IRouteGroup, method, path string, binding auth.EntryBinding) error {
	registry, module, err := EntryRegistryFrom(group)
	if err != nil {
		return err
	}
	if registry == nil {
		return errors.NewCode(errors.InvalidInput, "route has no authorization entry registry")
	}
	location, ok := group.(httpx.IRouteLocation)
	if !ok {
		return errors.NewCode(errors.InvalidInput, "route group requires actual path resolution")
	}
	binding.Module = module
	return registry.BindHTTP(method, location.RoutePath(path), binding)
}

// RegisterDeclaredRoute 登记具体用例动作并注册其 HTTP 处理器。
// 执行器负责业务授权和条件落实；处理器的动作映射由装配方保证。
func RegisterDeclaredRoute(group httpx.IRouteGroup, method, path string, handler httpx.Handler, execution IAuthorizationExecution, action string) error {
	if isNilEntryDependency(execution) || handler == nil {
		return errors.NewCode(errors.InvalidInput, "declared route requires an execution and handler")
	}
	binding, err := execution.AuthorizationBinding(action)
	if err != nil {
		return err
	}
	if err := BindEntry(group, method, path, binding); err != nil {
		return err
	}
	return RegisterRoute(group, method, path, handler)
}

func isNilEntryDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
