package assembly

import (
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtimecap"
	"gochen/errors"
)

// NewModule 从描述符创建模块（显式路径入口）。
//
// 这是框架的显式模块入口，不依赖反射魔法。
// 高级用户可直接使用此入口获得显式、可诊断、类型安全的模块定义。
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

// Init 初始化模块，注册所有 Registrations 到 DI 容器。
func (m *explicitModule) Init(opts module.ModuleInitOptions) error {
	m.opts = opts

	// 1. 准备容器与模块运行时状态。
	container, err := module.ResolveModuleContainer(opts)
	if err != nil {
		return runtimeutil.WrapModuleErr(m.desc.ID, err, "resolve container", errors.Internal)
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
