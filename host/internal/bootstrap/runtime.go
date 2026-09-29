package bootstrap

import (
	"gochen-runtime/di/basic"
	"reflect"
	"strings"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/http"
	"gochen-runtime/http/middleware"
	"gochen-runtime/http/nethttp"
	"gochen/errors"
	"gochen/eventing/bus"
	"gochen/eventing/monitoring"
	"gochen/eventing/projection"
	"gochen/httpx"
	"gochen/messaging"
)

// Config 描述 Host 运行期默认装配所需的外围组件。
type Config struct {
	Container di.IContainer

	Host     string
	Port     int
	BasePath string

	SecurityLayer            httpx.SecurityLayer
	AllowSession             *bool
	RouteMiddlewares         []httpx.Middleware
	DisableHealthRoute       bool
	EnableMonitoringRoutes   bool
	FailFastOnRouteConflicts bool

	HTTPServer        httpx.IServer
	EventBus          capability.IEventSubscriber
	Transport         capability.ITransport
	ProjectionRuntime capability.IProjectionRuntime
}

// Runtime 表示 Host 已就绪的外围运行时能力。
type Runtime struct {
	Container         di.IContainer
	HTTPServer        httpx.IServer
	BaseGroup         httpx.IRouteGroup
	EventBus          capability.IEventSubscriber
	Transport         capability.ITransport
	ProjectionRuntime capability.IProjectionRuntime
}

// Prepare 构造 Host 运行期需要的外围组件。
func Prepare(cfg Config) (*Runtime, error) {
	container := cfg.Container
	if container == nil {
		container = basic.New()
	}

	rt := &Runtime{Container: container}

	if err := prepareMessaging(rt, cfg); err != nil {
		return nil, err
	}
	if err := prepareHTTP(rt, cfg); err != nil {
		return nil, err
	}

	baseGroup, err := prepareBaseGroup(rt.HTTPServer, cfg)
	if err != nil {
		return nil, err
	}
	rt.BaseGroup = baseGroup

	projectionRuntime, err := resolveProjectionRuntime(cfg.ProjectionRuntime, container)
	if err != nil {
		return nil, err
	}
	rt.ProjectionRuntime = projectionRuntime
	if projectionRuntime != nil {
		if err := registerInstanceByType(container, (*capability.IProjectionRuntime)(nil), projectionRuntime); err != nil {
			return nil, err
		}
		if err := registerInstanceByType(container, (*projection.IProjectionRegistrar)(nil), projectionRuntime); err != nil {
			return nil, err
		}
	}

	return rt, nil
}

func prepareMessaging(rt *Runtime, cfg Config) error {
	if rt == nil || rt.Container == nil {
		return errors.NewCode(errors.Internal, "runtime container is nil")
	}

	if cfg.Transport != nil {
		rt.Transport = cfg.Transport
		if fullTransport, ok := rt.Transport.(messaging.ITransport); ok && !runtimeutil.IsTypedNil(fullTransport) {
			if err := registerInstanceByType(rt.Container, (*messaging.ITransport)(nil), fullTransport); err != nil {
				return err
			}
		}
	} else if tr, ok := tryResolveCapabilityTransport(rt.Container); ok {
		rt.Transport = tr
	} else if tr, ok := tryResolveTransport(rt.Container); ok {
		rt.Transport = tr
	}
	if rt.Transport != nil {
		if err := registerInstanceByType(rt.Container, (*capability.ITransport)(nil), rt.Transport); err != nil {
			return err
		}
	}

	if cfg.EventBus != nil {
		rt.EventBus = cfg.EventBus
		if eb, ok := rt.EventBus.(bus.IEventBus); ok && !runtimeutil.IsTypedNil(eb) {
			if err := registerInstanceByType(rt.Container, (*bus.IEventBus)(nil), eb); err != nil {
				return err
			}
		}
	} else if eb, ok := tryResolveEventBus(rt.Container); ok {
		rt.EventBus = eb
	}
	if rt.EventBus != nil {
		if err := registerInstanceByType(rt.Container, (*capability.IEventSubscriber)(nil), rt.EventBus); err != nil {
			return err
		}
	}

	return nil
}

func prepareHTTP(rt *Runtime, cfg Config) error {
	if rt == nil {
		return errors.NewCode(errors.Internal, "runtime is nil")
	}

	if cfg.HTTPServer != nil {
		rt.HTTPServer = cfg.HTTPServer
	} else {
		rt.HTTPServer = nethttp.NewServer(&http.WebConfig{
			Host:         cfg.Host,
			Port:         cfg.Port,
			ReadTimeout:  http.DefaultReadTimeout,
			WriteTimeout: http.DefaultWriteTimeout,
			IdleTimeout:  http.DefaultIdleTimeout,
		})
	}

	if rt.HTTPServer != nil && cfg.FailFastOnRouteConflicts {
		rt.HTTPServer = httpx.WithRouteRegistry(rt.HTTPServer)
	}

	return nil
}

func prepareBaseGroup(httpServer httpx.IServer, cfg Config) (httpx.IRouteGroup, error) {
	if httpServer == nil {
		return nil, nil
	}

	basePath := normalizeBasePath(cfg.BasePath)
	if basePath == "/" {
		basePath = ""
	}
	group := httpServer.Group(basePath)

	layer := cfg.SecurityLayer
	if layer == "" {
		layer = httpx.SecurityLayerAPI
	}
	allowSession := defaultAllowSessionForLayer(layer)
	if cfg.AllowSession != nil {
		allowSession = *cfg.AllowSession
	}

	baseMws := []httpx.Middleware{
		middleware.SecurityLayer(middleware.SecurityLayerConfig{
			Layer:        layer,
			AllowSession: allowSession,
		}),
	}
	if len(cfg.RouteMiddlewares) > 0 {
		baseMws = append(baseMws, cfg.RouteMiddlewares...)
	}
	group.Use(baseMws...)

	registerMonitoringRoutes(group, monitoring.HostRouteSet(!cfg.DisableHealthRoute, cfg.EnableMonitoringRoutes))

	return group, nil
}

func registerMonitoringRoutes(group httpx.IRouteGroup, set monitoring.RouteSet) {
	if group == nil {
		return
	}

	reg := monitoring.DefaultRegistry()
	for _, route := range monitoring.Routes(set) {
		route := route
		group.GET(route.Path, func(ctx httpx.IContext) error {
			code, body := monitoring.HandleRoute(ctx.RequestContext(), reg, route)
			return ctx.JSON(code, httpx.JSONValue(body))
		})
	}
}

func normalizeBasePath(basePath string) string {
	if basePath == "" {
		return "/api/v1"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	return basePath
}

func defaultAllowSessionForLayer(layer httpx.SecurityLayer) bool {
	switch layer {
	case httpx.SecurityLayerAPI:
		return false
	case httpx.SecurityLayerWeb:
		return true
	default:
		return false
	}
}

func tryResolveEventBus(container di.IResolver) (bus.IEventBus, bool) {
	if container == nil {
		return nil, false
	}
	inst, err := container.Resolve(reflect.TypeOf((*bus.IEventBus)(nil)).Elem())
	if err != nil {
		return nil, false
	}
	if eb, ok := inst.(bus.IEventBus); ok && !runtimeutil.IsTypedNil(eb) {
		return eb, true
	}
	return nil, false
}

func tryResolveTransport(container di.IResolver) (messaging.ITransport, bool) {
	if container == nil {
		return nil, false
	}
	inst, err := container.Resolve(reflect.TypeOf((*messaging.ITransport)(nil)).Elem())
	if err != nil {
		return nil, false
	}
	if tr, ok := inst.(messaging.ITransport); ok && !runtimeutil.IsTypedNil(tr) {
		return tr, true
	}
	return nil, false
}

func tryResolveCapabilityTransport(container di.IResolver) (capability.ITransport, bool) {
	if container == nil {
		return nil, false
	}
	inst, err := container.Resolve(reflect.TypeOf((*capability.ITransport)(nil)).Elem())
	if err != nil {
		return nil, false
	}
	if tr, ok := inst.(capability.ITransport); ok && !runtimeutil.IsTypedNil(tr) {
		return tr, true
	}
	return nil, false
}

func resolveProjectionRuntime(injected capability.IProjectionRuntime, container di.IResolver) (capability.IProjectionRuntime, error) {
	if injected != nil {
		return injected, nil
	}
	if container == nil {
		return nil, nil
	}

	// 注册接口通过返回的释放函数表达实例所有权，
	// 否则模块生命周期无法安全清理投影，宁可在此报错。
	pmIfaceType := reflect.TypeOf((*projection.IProjectionRegistrar)(nil)).Elem()
	pmAny, err := container.Resolve(pmIfaceType)
	if err != nil {
		return nil, nil
	}
	pm, ok := pmAny.(capability.IProjectionRuntime)
	if ok && !runtimeutil.IsTypedNil(pm) {
		return pm, nil
	}
	return nil, errors.NewCode(errors.Internal, "resolved projection registrar has invalid type").
		WithContext("service_type", di.TypeKey(pmIfaceType)).
		WithContext("type", runtimeutil.TypeString(pmAny))
}

func registerInstanceByType(container di.IContainer, ifacePtr any, instance any) error {
	if container == nil || ifacePtr == nil || instance == nil {
		return nil
	}

	t := reflect.TypeOf(ifacePtr)
	if t.Kind() != reflect.Ptr {
		return nil
	}
	iface := t.Elem()
	if container.IsRegistered(iface) {
		return nil
	}

	instValue := reflect.ValueOf(instance)
	if !instValue.IsValid() {
		return nil
	}
	instType := instValue.Type()
	if !instType.AssignableTo(iface) {
		return errors.NewCode(errors.InvalidInput, "instance type is not assignable to target type").
			WithContext("target_type", di.TypeKey(iface)).
			WithContext("instance_type", di.TypeKey(instType)).
			WithContext("instance_type_raw", instType.String())
	}
	return container.RegisterInstance(iface, di.NewInstance(instance))
}
