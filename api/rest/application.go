package rest

import (
	"context"

	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/domain"
	"gochen/httpx"
)

// Application 是 REST 挂载所需的接口，契约由 app 层持有（app/crud.IApplication）。
//
// 这里只是别名：rest 不再维护一份需要手工同步的 16 方法副本。
// 路由集合完全由 Descriptor 决定：Declared 的动作才注册标准路由；
// 生命周期动作（restore / purge / list_deleted）要求应用实现 LifecycleApplication。
type Application[T domain.IEntity[ID], ID comparable] = appcrud.IApplication[T, ID]

// LifecycleApplication 是 audited 应用的生命周期入口（app/audited.ILifecycleApplication）。
type LifecycleApplication[T domain.IEntity[ID], ID comparable] = appaudited.ILifecycleApplication[T, ID]

// ActionHandler 是业务动作路由的处理器。
//
// rest.Action 只负责动作声明校验、操作人注入与错误映射；解码与命令编排
// 由 handler 调用 app.Execute / app.ExecuteNew / app.Read 完成。
type ActionHandler[T domain.IEntity[ID], ID comparable] func(ctx context.Context, c httpx.IContext, app Application[T, ID]) error
