package runtimecap

import (
	"sync"

	"gochen-runtime/host/capability"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/initcap"
	"gochen/httpx"
)

var (
	metadataRegistryKey  = initcap.NewKey[capability.IMetadataRegistry]("host.module.runtimecap.metadata_registry")
	eventBusKey          = initcap.NewKey[capability.IEventSubscriber]("host.module.runtimecap.event_bus")
	projectionRuntimeKey = initcap.NewKey[capability.IProjectionRuntime]("host.module.runtimecap.projection_runtime")
	transportKey         = initcap.NewKey[capability.ITransport]("host.module.runtimecap.transport")
)

// ModuleHTTPOptions 定义模块的 HTTP 挂载选项。
//
// 约定：
// - BaseGroup 由 Host 创建并注入（包含 BasePath 与全局中间件）；
// - 模块可基于 BaseGroup 派生自己的子 group（Prefix + Middlewares）；
// - 若 BaseGroup 为空，视为未启用 HTTP。
type ModuleHTTPOptions struct {
	baseGroup   httpx.IRouteGroup
	prefix      string
	middlewares []httpx.Middleware

	mountOnce  sync.Once
	mountGroup httpx.IRouteGroup
}

// NewModuleHTTPOptions 创建模块 HTTP 挂载选项。
func NewModuleHTTPOptions(baseGroup httpx.IRouteGroup, prefix string, middlewares ...httpx.Middleware) *ModuleHTTPOptions {
	return &ModuleHTTPOptions{
		baseGroup:   baseGroup,
		prefix:      prefix,
		middlewares: append([]httpx.Middleware(nil), middlewares...),
	}
}

// Prefix 返回模块挂载前缀。
func (o *ModuleHTTPOptions) Prefix() string {
	if o == nil {
		return ""
	}
	return o.prefix
}

// Middlewares 返回模块级 HTTP 中间件副本。
func (o *ModuleHTTPOptions) Middlewares() []httpx.Middleware {
	if o == nil || len(o.middlewares) == 0 {
		return nil
	}
	return append([]httpx.Middleware(nil), o.middlewares...)
}

// MountGroup 返回“模块实际应挂载的 group”。
func (o *ModuleHTTPOptions) MountGroup() httpx.IRouteGroup {
	if o == nil || o.baseGroup == nil {
		return nil
	}
	o.mountOnce.Do(func() {
		g := o.baseGroup
		if o.prefix == "" || o.prefix == "/" {
			g = g.Group("")
		} else {
			g = g.Group(o.prefix)
		}
		if len(o.middlewares) > 0 {
			g.Use(o.middlewares...)
		}
		o.mountGroup = g
	})
	return o.mountGroup
}

// HTTP 创建 HTTP capability setter。
func HTTP(http *ModuleHTTPOptions) initcap.Setter {
	return initcap.Set(module.HTTPKey, module.IHTTPOptions(http))
}

// WithHTTP 注入模块 HTTP 挂载能力。
func WithHTTP(opts module.ModuleInitOptions, http *ModuleHTTPOptions) module.ModuleInitOptions {
	return module.WithCapability(opts, module.HTTPKey, module.IHTTPOptions(http))
}

// HTTPFrom 读取模块 HTTP 挂载能力。
//
// 架构契约与类型转换说明：
//  1. 宿主（runtime.Host）在初始化阶段始终注入具体的 *ModuleHTTPOptions 实例；
//  2. 底层 module.HTTPKey 使用通用的 module.IHTTPOptions 接口是为了允许轻量级包（如 quick 门面包）
//     在不引入本包依赖的前提下直接调用 MountGroup()，彻底消除循环依赖；
//  3. 此处安全向下转型回 *ModuleHTTPOptions，既保留了既有调用方对 Prefix() / Middlewares()
//     等高级配置项的访问能力，又是类型安全的。
func HTTPFrom(opts module.ModuleInitOptions) *ModuleHTTPOptions {
	iface, ok := module.CapabilityFrom(opts, module.HTTPKey)
	if !ok || iface == nil {
		return nil
	}
	if typed, ok := iface.(*ModuleHTTPOptions); ok {
		return typed
	}
	return nil
}

// ModuleRegistry 创建模块目录 capability setter。
func ModuleRegistry(registry capability.IModuleRegistry) initcap.Setter {
	return initcap.Set(module.ModuleRegistryKey, module.IModuleRegistry(registry))
}

// WithModuleRegistry 注入模块目录能力。
func WithModuleRegistry(opts module.ModuleInitOptions, registry capability.IModuleRegistry) module.ModuleInitOptions {
	return module.WithCapability(opts, module.ModuleRegistryKey, module.IModuleRegistry(registry))
}

// ModuleRegistryFrom 读取模块目录能力。
func ModuleRegistryFrom(opts module.ModuleInitOptions) capability.IModuleRegistry {
	registry, _ := module.CapabilityFrom(opts, module.ModuleRegistryKey)
	return registry
}

// MetadataRegistry 创建事件溯源聚合 metadata registry capability setter。
func MetadataRegistry(registry capability.IMetadataRegistry) initcap.Setter {
	return initcap.Set(metadataRegistryKey, registry)
}

// WithMetadataRegistry 注入事件溯源聚合 metadata registry 能力。
func WithMetadataRegistry(opts module.ModuleInitOptions, registry capability.IMetadataRegistry) module.ModuleInitOptions {
	return module.WithCapability(opts, metadataRegistryKey, registry)
}

// MetadataRegistryFrom 读取事件溯源聚合 metadata registry。
func MetadataRegistryFrom(opts module.ModuleInitOptions) capability.IMetadataRegistry {
	registry, _ := module.CapabilityFrom(opts, metadataRegistryKey)
	return registry
}

// EventBus 创建事件订阅 capability setter。
func EventBus(eventBus capability.IEventSubscriber) initcap.Setter {
	return initcap.Set(eventBusKey, eventBus)
}

// WithEventBus 注入模块事件订阅能力。
func WithEventBus(opts module.ModuleInitOptions, eventBus capability.IEventSubscriber) module.ModuleInitOptions {
	return module.WithCapability(opts, eventBusKey, eventBus)
}

// EventBusFrom 读取模块事件订阅能力。
func EventBusFrom(opts module.ModuleInitOptions) capability.IEventSubscriber {
	eventBus, _ := module.CapabilityFrom(opts, eventBusKey)
	return eventBus
}

// ProjectionRuntime 创建投影注册 capability setter。
func ProjectionRuntime(runtime capability.IProjectionRuntime) initcap.Setter {
	return initcap.Set(projectionRuntimeKey, runtime)
}

// WithProjectionRuntime 注入模块投影注册能力。
func WithProjectionRuntime(opts module.ModuleInitOptions, runtime capability.IProjectionRuntime) module.ModuleInitOptions {
	return module.WithCapability(opts, projectionRuntimeKey, runtime)
}

// ProjectionRuntimeFrom 读取模块投影注册能力。
func ProjectionRuntimeFrom(opts module.ModuleInitOptions) capability.IProjectionRuntime {
	runtime, _ := module.CapabilityFrom(opts, projectionRuntimeKey)
	return runtime
}

// Transport 创建消息传输 capability setter。
func Transport(transport capability.ITransport) initcap.Setter {
	return initcap.Set(transportKey, transport)
}

// WithTransport 注入模块消息传输能力。
func WithTransport(opts module.ModuleInitOptions, transport capability.ITransport) module.ModuleInitOptions {
	return module.WithCapability(opts, transportKey, transport)
}

// TransportFrom 读取模块消息传输能力。
func TransportFrom(opts module.ModuleInitOptions) capability.ITransport {
	transport, _ := module.CapabilityFrom(opts, transportKey)
	return transport
}
