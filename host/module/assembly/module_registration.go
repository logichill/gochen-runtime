package assembly

import (
	"reflect"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	"gochen/errors"
)

// registerOne 注册单个 Registration 到容器。
func (m *explicitModule) registerOne(container module.IModuleContainer, reg Registration, index int) error {
	if reg.Factory.IsZero() && reg.Instance == nil {
		return nil // 空注册项，跳过
	}

	serviceType, err := m.resolveRegistrationServiceType(reg)
	if err != nil {
		return runtimeutil.WrapModuleErr(m.desc.ID, err, "resolve registration type", errors.Internal).
			WithContext("index", index)
	}

	switch reg.Lifetime {
	case module.SingletonLifetime:
		if reg.Instance != nil {
			if err := container.RegisterInstance(serviceType, di.NewInstance(reg.Instance)); err != nil {
				return runtimeutil.WrapModuleErr(m.desc.ID, err, "register instance", errors.Internal).
					WithContext("index", index).
					WithContext("service_type", di.TypeKey(serviceType))
			}
		} else if !reg.Factory.IsZero() {
			if err := container.RegisterSingleton(serviceType, reg.Factory); err != nil {
				return runtimeutil.WrapModuleErr(m.desc.ID, err, "register singleton", errors.Internal).
					WithContext("index", index).
					WithContext("service_type", di.TypeKey(serviceType))
			}
		}
	case module.TransientLifetime:
		if reg.Instance != nil {
			// 瞬态不能直接注册实例
			return errors.NewCode(errors.InvalidInput, "transient registration cannot use Instance").
				WithContext("module", m.desc.ID).
				WithContext("index", index)
		} else if !reg.Factory.IsZero() {
			if err := container.RegisterTransient(serviceType, reg.Factory); err != nil {
				return runtimeutil.WrapModuleErr(m.desc.ID, err, "register transient", errors.Internal).
					WithContext("index", index).
					WithContext("service_type", di.TypeKey(serviceType))
			}
		}
	default:
		return errors.NewCode(errors.InvalidInput, "unknown lifetime").
			WithContext("module", m.desc.ID).
			WithContext("index", index).
			WithContext("lifetime", reg.Lifetime)
	}

	return nil
}

func (m *explicitModule) resolveRegistrationServiceType(reg Registration) (reflect.Type, error) {
	serviceType := reg.ServiceType
	if serviceType == nil {
		return nil, errors.NewCode(errors.InvalidInput, "registration service type is nil")
	}

	if reg.Instance != nil {
		instType := reflect.TypeOf(reg.Instance)
		if instType == nil {
			return nil, errors.NewCode(errors.InvalidInput, "instance type is nil")
		}
		if !isRegistrationTypeAssignable(instType, serviceType) {
			return nil, errors.NewCode(errors.InvalidInput, "instance is not assignable to registration service type").
				WithContext("instance_type", instType.String()).
				WithContext("service_type", serviceType.String())
		}
		return serviceType, nil
	}

	return serviceType, nil
}

// classifyByRole 按角色分类存储 Registration。
func (m *explicitModule) classifyByRole(index int, reg Registration) {
	for _, role := range reg.Roles {
		switch role {
		case RoleRouteRegistrar:
			m.routeRegistrarRegs = append(m.routeRegistrarRegs, reg)
		case RoleEventHandler:
			m.runtime.AddEventHandler(index, m.makeRuntimeResolver(reg))
		case RoleProjection:
			m.runtime.AddProjection(index, m.makeRuntimeResolver(reg))
		case RoleRuntimeComponent:
			m.runtime.AddRuntimeComponent(index, m.makeRuntimeResolver(reg))
		}
	}
}

func (m *explicitModule) makeRuntimeResolver(reg Registration) capability.ResolveFunc {
	regCopy := reg
	return func() (any, error) {
		return m.resolveFromReg(regCopy)
	}
}
func isRegistrationTypeAssignable(sourceType, targetType reflect.Type) bool {
	if sourceType == nil || targetType == nil {
		return false
	}
	if sourceType.AssignableTo(targetType) {
		return true
	}
	if targetType.Kind() == reflect.Interface && sourceType.Implements(targetType) {
		return true
	}
	return false
}
