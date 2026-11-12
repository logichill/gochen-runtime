package assembly

import (
	"context"

	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	runtimehttp "gochen-runtime/http"
	"gochen/errors"
)

// RegisterRoutes 注册模块的 HTTP 路由。
func (m *explicitModule) RegisterRoutes(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	httpOptions, _ := module.CapabilityFrom(m.opts, module.HTTPKey)
	if runtimeutil.IsTypedNil(httpOptions) {
		return nil
	}
	group := httpOptions.MountGroup()
	if group == nil {
		// HTTP 未启用，跳过路由注册
		return nil
	}
	registry, _, err := runtimehttp.EntryRegistryFrom(group)
	if err != nil {
		return err
	}
	if registry != nil {
		group, err = runtimehttp.WithEntryRegistry(group, registry, m.desc.ID)
		if err != nil {
			return err
		}
	}

	// 1. 应用模块级中间件。
	if len(m.desc.Middlewares) > 0 {
		group.Use(m.desc.Middlewares...)
	}

	// 2. 按角色分类的路由注册器逐个解析并调用 RegisterRoutes。
	for i, reg := range m.routeRegistrarRegs {
		inst, err := m.resolveFromReg(reg)
		if err != nil {
			return runtimeutil.WrapModuleErr(m.desc.ID, err, "resolve route registrar", errors.Internal).
				WithContext("index", i)
		}

		registrar, ok := inst.(module.IRouteRegistrar)
		if !ok {
			return errors.NewCode(errors.Internal, "route registrar does not implement IRouteRegistrar").
				WithContext("module", m.desc.ID).
				WithContext("index", i)
		}
		if err := registrar.RegisterRoutes(group.Group("")); err != nil {
			return runtimeutil.WrapModuleErr(m.desc.ID, err, "register routes", errors.Dependency).
				WithContext("index", i)
		}
	}

	return nil
}

// resolveFromReg 从 Registration 解析实例。
func (m *explicitModule) resolveFromReg(reg Registration) (interface{}, error) {
	if reg.Instance != nil {
		return reg.Instance, nil
	}

	serviceType, err := m.resolveRegistrationServiceType(reg)
	if err != nil {
		return nil, err
	}
	return m.container.Resolve(serviceType)
}

// RuntimeComponents 返回启动前解析并固定的真实组件实例。
func (m *explicitModule) RuntimeComponents() ([]capability.IRuntimeComponent, error) {
	if m.runtime == nil {
		return nil, errors.NewCode(errors.FailedPrecondition, "module runtime requires initialization")
	}
	return m.runtime.RuntimeComponents()
}

// RuntimeEventHandlers 取得与实际订阅相同的处理器实例及冻结消息类型。
func (m *explicitModule) RuntimeEventHandlers() ([]capability.EventHandlerRegistration, error) {
	if m.runtime == nil {
		return nil, errors.NewCode(errors.FailedPrecondition, "module runtime requires initialization")
	}
	return m.runtime.RuntimeEventHandlers()
}

// SealEventHandlers 封存实际订阅集合。
func (m *explicitModule) SealEventHandlers() error { return m.runtime.SealEventHandlers() }

// ActivateEventHandlers 在启动门禁通过后启用实际订阅集合。
func (m *explicitModule) ActivateEventHandlers() error { return m.runtime.ActivateEventHandlers() }

// Start 启动模块，订阅事件处理器并启动投影。
func (m *explicitModule) Start(ctx context.Context) (module.ModuleStopFunc, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if m.onStopPending {
		return nil, errors.NewCode(errors.Conflict, "module has pending OnStop cleanup").
			WithContext("module", m.desc.ID)
	}
	if m.runtime != nil {
		// 启动走到钩子阶段即视为已进入：无论是否配置 OnStart 都先标记，
		// 确保钩子中途失败或仅配置 OnStop 的模块都能收到配对的 OnStop。
		onStart := func(ctx context.Context) error {
			if m.desc.OnStop != nil {
				m.onStopPending = true
			}
			if m.desc.OnStart == nil {
				return nil
			}
			return m.desc.OnStart(ctx)
		}
		if err := m.runtime.Start(ctx, onStart); err != nil {
			// 返回完整停止闭包，使 Host 能接管部分启动后的资源并重试清理。
			return m.stop, err
		}
	}
	return m.stop, nil
}

// stop 停止模块，清理资源。
func (m *explicitModule) stop(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if m.runtime == nil {
		if !m.onStopPending || m.desc.OnStop == nil {
			return nil
		}
		if err := m.desc.OnStop(ctx); err != nil {
			return runtimeutil.WrapModuleErr(m.desc.ID, err, "OnStop", errors.Internal)
		}
		m.onStopPending = false
		return nil
	}

	var onStop func(context.Context) error
	hookSucceeded := false
	if m.onStopPending && m.desc.OnStop != nil {
		onStop = func(ctx context.Context) error {
			err := m.desc.OnStop(ctx)
			if err == nil {
				hookSucceeded = true
			}
			return err
		}
	}
	err := m.runtime.Stop(ctx, onStop)
	if hookSucceeded {
		m.onStopPending = false
	}
	return err
}

// 确保 explicitModule 实现 IRouteModule
var _ module.IRouteModule = (*explicitModule)(nil)
