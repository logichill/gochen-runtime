package capability

import (
	"context"
	"gochen/httpx"
)

// IEntryValidator 在路由完成后、后台组件和传输启动前核对独立注册清单。
type IEntryValidator interface {
	ValidateEntries(context.Context, httpx.IServer) error
}

// IModuleEntryCollector 在运行期开始前收集模块与实际组件的入口。
type IModuleEntryCollector interface {
	PrepareModuleEntries(context.Context, []ModuleCatalogEntry) error
}

// IEntryActivator 在全部启动门禁通过后启用已核对的处理器。
type IEntryActivator interface {
	ActivateEntries() error
}

// IRuntimeComponentProvider 暴露与 Start 使用相同实例的运行期组件。
// 组件必须具有非空指针身份；Host 在启动前拒绝重复生命周期所有者。
type IRuntimeComponentProvider interface {
	RuntimeComponents() ([]IRuntimeComponent, error)
}
