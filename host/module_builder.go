package host

import (
	"context"
	"reflect"

	"gochen-runtime/di"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/assembly"
	"gochen-runtime/host/module/runtimecap"
	"gochen/domain/eventsourced"
	"gochen/errors"
	"gochen/httpx"
)

// AggregateConfig 描述一个需要在模块装配阶段 fail-fast 校验的 event-sourced 聚合。
type AggregateConfig = eventsourced.MetadataRegistration

// Aggregate 创建 AggregateConfig，供 Host 模块配置复用。
func Aggregate(sample any, aggregateType string) AggregateConfig {
	return eventsourced.MetadataRegistration{
		Sample:        sample,
		AggregateType: aggregateType,
	}
}

// AggregateFromTag 创建 AggregateConfig，aggregateType 从 sample 的 struct tag 自动提取。
func AggregateFromTag(sample any) AggregateConfig {
	aggregateType, err := eventsourced.ResolveAggregateType(sample)
	return eventsourced.MetadataRegistration{
		Sample:        sample,
		AggregateType: aggregateType,
		Error:         err,
	}
}

// Builder 定义 Host 根入口的链式模块构造器。
type Builder struct {
	id                string
	name              string
	providers         []any
	aggregates        []AggregateConfig
	extensions        []any
	routeRegistrars   []any
	eventHandlers     []any
	projections       []any
	runtimeComponents []any
	middlewares       []httpx.Middleware
	onStart           func(ctx context.Context) error
	onStop            func(ctx context.Context) error
}

// Module 创建一个链式模块构造器。
func Module(id string) *Builder {
	return &Builder{id: id}
}

// Name 设置模块展示名。
func (b *Builder) Name(name string) *Builder {
	b.name = name
	return b
}

// Provide 追加 DI provider。
func (b *Builder) Provide(providers ...any) *Builder {
	b.providers = append(b.providers, providers...)
	return b
}

// Aggregate 追加需要 fail-fast 校验与 Init 预热的聚合声明。
func (b *Builder) Aggregate(aggregates ...AggregateConfig) *Builder {
	b.aggregates = append(b.aggregates, aggregates...)
	return b
}

// Extension 追加模块的可选能力扩展载荷。
//
// 基础 Host 不解释载荷内容，只在目录登记阶段原样透出给已注册的
// capability.IModuleCatalogRegistrar。权限目录请使用
// runtime/host/authz.Catalog，例如：
//
//	host.Module("order").Extension(authz.Catalog{Permissions: []string{"order:api:read"}})
func (b *Builder) Extension(extensions ...any) *Builder {
	for _, ext := range extensions {
		if ext != nil {
			b.extensions = append(b.extensions, ext)
		}
	}
	return b
}

// RouteRegistrar 追加路由注册器构造器。
func (b *Builder) RouteRegistrar(registrars ...any) *Builder {
	b.routeRegistrars = append(b.routeRegistrars, registrars...)
	return b
}

// EventHandler 追加事件处理器构造器。
func (b *Builder) EventHandler(handlers ...any) *Builder {
	b.eventHandlers = append(b.eventHandlers, handlers...)
	return b
}

// Projection 追加投影构造器。
func (b *Builder) Projection(projections ...any) *Builder {
	b.projections = append(b.projections, projections...)
	return b
}

// RuntimeComponent 追加运行期组件构造器。
func (b *Builder) RuntimeComponent(components ...any) *Builder {
	b.runtimeComponents = append(b.runtimeComponents, components...)
	return b
}

// Middleware 追加模块级 HTTP 中间件。
func (b *Builder) Middleware(middlewares ...httpx.Middleware) *Builder {
	b.middlewares = append(b.middlewares, middlewares...)
	return b
}

// OnStart 设置模块启动钩子。
func (b *Builder) OnStart(fn func(ctx context.Context) error) *Builder {
	b.onStart = fn
	return b
}

// OnStop 设置模块停止钩子。
func (b *Builder) OnStop(fn func(ctx context.Context) error) *Builder {
	b.onStop = fn
	return b
}

// Build 根据构造器状态生成模块实例。
func (b *Builder) Build() (IModule, error) {
	desc, err := b.descriptor()
	if err != nil {
		return nil, err
	}
	return assembly.NewModule(desc), nil
}

func (b *Builder) descriptor() (assembly.ModuleDescriptor, error) {
	return convertBuilderToDescriptor(b)
}

func convertBuilderToDescriptor(builder *Builder) (assembly.ModuleDescriptor, error) {
	if builder == nil {
		return assembly.ModuleDescriptor{}, errors.NewCode(errors.InvalidInput, "builder cannot be nil")
	}

	desc := assembly.ModuleDescriptor{
		ID:          builder.id,
		Name:        builder.name,
		Extensions:  append([]any(nil), builder.extensions...),
		OnInit:      buildModuleInitHook(builder.id, builder.aggregates),
		Middlewares: append([]httpx.Middleware(nil), builder.middlewares...),
		OnStart:     builder.onStart,
		OnStop:      builder.onStop,
	}

	if err := validateAggregateConfigs(builder.id, builder.aggregates); err != nil {
		return assembly.ModuleDescriptor{}, err
	}

	appendRegistrations := func(field string, providers []any, roles ...assembly.Role) error {
		for i, provider := range providers {
			if provider == nil {
				continue
			}
			serviceType, err := inferModuleServiceType(builder.id, field, i, provider)
			if err != nil {
				return err
			}
			desc.Registrations = append(desc.Registrations, assembly.Registration{
				Lifetime:    module.SingletonLifetime,
				ServiceType: serviceType,
				Factory:     di.NewFactory(provider),
				Roles:       append([]assembly.Role(nil), roles...),
			})
		}
		return nil
	}

	if err := appendRegistrations("providers", builder.providers); err != nil {
		return assembly.ModuleDescriptor{}, err
	}
	if err := appendRegistrations("route_registrars", builder.routeRegistrars, assembly.RoleRouteRegistrar); err != nil {
		return assembly.ModuleDescriptor{}, err
	}
	if err := appendRegistrations("event_handlers", builder.eventHandlers, assembly.RoleEventHandler); err != nil {
		return assembly.ModuleDescriptor{}, err
	}
	if err := appendRegistrations("projections", builder.projections, assembly.RoleProjection); err != nil {
		return assembly.ModuleDescriptor{}, err
	}
	if err := appendRegistrations("runtime_components", builder.runtimeComponents, assembly.RoleRuntimeComponent); err != nil {
		return assembly.ModuleDescriptor{}, err
	}

	return desc, nil
}

func validateAggregateConfigs(moduleID string, aggregates []AggregateConfig) error {
	if err := eventsourced.ValidateMetadataSet(aggregates...); err != nil {
		var appErr *errors.AppError
		if errors.As(err, &appErr) && appErr != nil {
			return appErr.Wrap("validate aggregate metadata").
				WithContext("module", moduleID).
				WithContext("field", "aggregates")
		}
		return errors.Wrap(err, errors.InvalidInput, "validate aggregate metadata").
			WithContext("module", moduleID).
			WithContext("field", "aggregates")
	}
	return nil
}

func buildModuleInitHook(
	moduleID string,
	aggregates []AggregateConfig,
) func(module.ModuleInitOptions) error {
	return buildAggregateInitHook(moduleID, aggregates)
}

func buildAggregateInitHook(moduleID string, aggregates []AggregateConfig) func(module.ModuleInitOptions) error {
	if len(aggregates) == 0 {
		return nil
	}

	copied := append([]AggregateConfig(nil), aggregates...)
	return func(opts module.ModuleInitOptions) error {
		registry := runtimecap.MetadataRegistryFrom(opts)
		if registry == nil {
			return errors.NewCode(errors.InvalidInput, "metadata registry cannot be nil").
				WithContext("module", moduleID).
				WithContext("field", "aggregates")
		}
		if err := registry.RegisterSet(copied...); err != nil {
			var appErr *errors.AppError
			if errors.As(err, &appErr) && appErr != nil {
				return appErr.Wrap("register aggregate metadata").
					WithContext("module", moduleID).
					WithContext("field", "aggregates")
			}
			return errors.Wrap(err, errors.InvalidInput, "register aggregate metadata").
				WithContext("module", moduleID).
				WithContext("field", "aggregates")
		}
		return nil
	}
}

func inferModuleServiceType(moduleID string, field string, index int, provider any) (reflect.Type, error) {
	serviceType, err := resolveProviderOutputType(provider)
	if err != nil {
		var appErr *errors.AppError
		if errors.As(err, &appErr) && appErr != nil {
			return nil, appErr.Wrap("infer module service type").
				WithContext("module", moduleID).
				WithContext("field", field).
				WithContext("index", index)
		}
		return nil, errors.Wrap(err, errors.InvalidInput, "infer module service type").
			WithContext("module", moduleID).
			WithContext("field", field).
			WithContext("index", index)
	}
	return serviceType, nil
}

func resolveProviderOutputType(provider any) (reflect.Type, error) {
	t := reflect.TypeOf(provider)
	if t == nil || t.Kind() != reflect.Func {
		return nil, errors.NewCode(errors.InvalidInput, "provider must be a function")
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	switch t.NumOut() {
	case 1:
		if t.Out(0).Implements(errorType) {
			return nil, errors.NewCode(errors.InvalidInput, "provider must not return only error")
		}
		return t.Out(0), nil
	case 2:
		if !t.Out(1).Implements(errorType) {
			return nil, errors.NewCode(errors.InvalidInput, "provider second return value must be error")
		}
		return t.Out(0), nil
	default:
		return nil, errors.NewCode(errors.InvalidInput, "provider must return (T) or (T, error)")
	}
}
