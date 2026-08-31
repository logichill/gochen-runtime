package assembly

import (
	"context"

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
			return wrapModuleErr(m.desc.ID, err, "resolve route registrar").
				WithContext("index", i)
		}

		registrar, ok := inst.(IRouteRegistrar)
		if !ok {
			return errors.NewCode(errors.Internal, "route registrar does not implement IRouteRegistrar").
				WithContext("module", m.desc.ID).
				WithContext("index", i)
		}
		if err := registrar.RegisterRoutes(group); err != nil {
			var appErr *errors.AppError
			if errors.As(err, &appErr) && appErr != nil {
				return appErr.Wrap("register routes").WithContext("module", m.desc.ID).WithContext("index", i)
			}
			return errors.Wrap(err, errors.Dependency, "register routes").
				WithContext("module", m.desc.ID).
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
	if m.runtime != nil {
		if err := m.runtime.Start(ctx, m.desc.OnStart); err != nil {
			return nil, err
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
		if m.desc.OnStop != nil {
			if err := m.desc.OnStop(ctx); err != nil {
				return wrapModuleErr(m.desc.ID, err, "OnStop")
			}
		}
		return nil
	}
	return m.runtime.Stop(ctx, m.desc.OnStop)
}

// Ensure explicitModule implements IRouteModule
var _ module.IRouteModule = (*explicitModule)(nil)
