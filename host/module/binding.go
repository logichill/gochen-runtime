package module

import "gochen/app"

// Binding 描述一个入口对模块的显式贡献，不构造服务或启动后台任务。
// Applications 保留实际应用或同一命令派生的资源描述；RouteRegistrars 保留对应入口。
type Binding struct {
	Applications    []app.IDescriptorProvider
	RouteRegistrars []IRouteRegistrar
}

// IBinding 在 Build 阶段提供已装配入口及其应用声明。
// ModuleBinding 只能校验和描述装配，不得执行用例、登记路由或获取外部资源。
// 允许重复调用；返回的集合由 Builder 复制，实例由组合根持有。
type IBinding interface {
	ModuleBinding() (Binding, error)
}
