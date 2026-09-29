package assembly

import (
	"context"

	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtimecap"
	"gochen/errors"
)

// RegisterRoutes 注册模块的 HTTP 路由。
func (m *explicitModule) RegisterRoutes(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	httpOptions := runtimecap.HTTPFrom(m.opts)
	if httpOptions == nil {
		return nil
	}
	group := httpOptions.MountGroup()
	if group == nil {
		// HTTP 未启用，跳过路由注册
		return nil
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

		registrar, ok := inst.(IRouteRegistrar)
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
