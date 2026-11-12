package assembly

import (
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtimecap"
	"gochen/app"
	"gochen/errors"
)

// NewModule 从宿主内部描述符创建正式模块。公开声明入口为 host.Module。
func NewModule(desc ModuleDescriptor) module.IModule {
	return &explicitModule{desc: desc}
}

// explicitModule 基于 ModuleDescriptor 的显式模块实现。
type explicitModule struct {
	desc      ModuleDescriptor
	container module.IModuleContainer
	opts      module.ModuleInitOptions

	// 分类存储带角色的注册项（Init 阶段填充）
	routeRegistrarRegs []Registration
	runtime            *capability.ModuleRuntime
	onStopPending      bool
}

func (m *explicitModule) ID() string {
	return m.desc.ID
}

func (m *explicitModule) Name() string {
	return m.desc.Name
}

// ModuleExtensions 返回模块声明的可选能力扩展载荷。
func (m *explicitModule) ModuleExtensions() []any {
	return append([]any(nil), m.desc.Extensions...)
}

// Init 注册高级 DI 服务，并绑定直接持有的角色实例。
func (m *explicitModule) Init(opts module.ModuleInitOptions) error {
	m.opts = opts

	// 1. 准备容器与模块运行时状态。
	var container module.IModuleContainer
	for _, reg := range m.desc.Registrations {
		if reg.Instance != nil && len(reg.Roles) > 0 {
			continue
		}
		var err error
		container, err = module.ResolveModuleContainer(opts)
		if err != nil {
			return runtimeutil.WrapModuleErr(m.desc.ID, err, "resolve container", errors.Internal)
		}
		break
	}
	m.container = container
	m.runtime = capability.NewModuleRuntime(
		m.desc.ID,
		capability.NewRuntime(runtimecap.EventBusFrom(opts), runtimecap.ProjectionRuntimeFrom(opts), runtimecap.TransportFrom(opts)),
	)

	// 2. 分类注册 Registrations，并按角色分组存储。
	for i, reg := range m.desc.Registrations {
		if err := m.registerOne(container, reg, i); err != nil {
			return err
		}
		m.classifyByRole(i, reg)
	}

	if m.desc.OnInit != nil {
		if err := m.desc.OnInit(opts); err != nil {
			return runtimeutil.WrapModuleErr(m.desc.ID, err, "module init hook", errors.Internal)
		}
	}

	return nil
}

// Applications 返回应用声明的独立集合。
func (m *explicitModule) Applications() []app.IDescriptorProvider {
	return append([]app.IDescriptorProvider(nil), m.desc.Applications...)
}

// DependsOn 返回模块依赖的独立集合。
func (m *explicitModule) DependsOn() []string { return append([]string(nil), m.desc.Dependencies...) }

// PublicFeatures 返回受限公开能力的独立集合。
func (m *explicitModule) PublicFeatures() []module.PublicFeature {
	return append([]module.PublicFeature(nil), m.desc.Features...)
}
