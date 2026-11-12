package module

import (
	"gochen-runtime/host/module/initcap"
	"gochen/app"
)

// IApplicationProvider 暴露模块提供的应用实例，包含没有 HTTP 入口的应用。
// Host 在 Init 后登记，返回的实例必须与实际用例入口使用的实例相同。
type IApplicationProvider interface {
	Applications() []app.IDescriptorProvider
}

// IApplicationRegistry 是模块启动期登记应用的窄端口。
// Register 原子检查资源动作冲突；应用描述在登记后不得修改。
// 路由登记结束后冻结，运行期继续登记将返回 FailedPrecondition。
type IApplicationRegistry interface {
	Register(...app.IDescriptorProvider) error
}

// ApplicationRegistryKey 标识 Host 实例独立的应用登记能力。
var ApplicationRegistryKey = initcap.NewKey[IApplicationRegistry]("host.module.applications")

// ApplicationRegistryFrom 返回初始化阶段可使用的应用登记端口。
func ApplicationRegistryFrom(opts ModuleInitOptions) IApplicationRegistry {
	registry, _ := CapabilityFrom(opts, ApplicationRegistryKey)
	return registry
}
