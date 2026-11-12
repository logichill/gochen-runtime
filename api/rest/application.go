package rest

import (
	"gochen/app"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/domain"
)

// ReadApplication 组合标准 REST 挂载所需的读取、描述与分页能力。
// 写入、审计和生命周期能力仅在 Descriptor 声明对应动作时要求提供。
type ReadApplication[T domain.IEntity[ID], ID comparable] interface {
	app.IDescriptorProvider
	appcrud.IReadApplication[T, ID]
	MaxPageSize() int
}

// Application 是完整 CRUD 用例契约，供需要写入与自定义执行的调用方使用。
// 标准路由挂载只要求 ReadApplication，并在装配期校验声明的可选能力。
type Application[T domain.IEntity[ID], ID comparable] = appcrud.IApplication[T, ID]

// LifecycleApplication 是 audited 应用的生命周期入口（app/audited.ILifecycleApplication）。
type LifecycleApplication[T domain.IEntity[ID], ID comparable] = appaudited.ILifecycleApplication[T, ID]
