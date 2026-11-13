package rest

import (
	"gochen/app"
	appcrud "gochen/app/crud"
	appaudit "gochen/app/crud/audit"
	"gochen/app/crud/deletion"
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

// DeletionApplication 是独立删除生命周期及其授权描述。
type DeletionApplication[T domain.IEntity[ID], ID comparable] interface {
	app.IDescriptorProvider
	deletion.IDeletionApplication[T, ID]
	MaxPageSize() int
	MaxBulkSize() int
}

// AuditApplication 是独立审计历史及其授权描述，不要求实体读取能力。
type AuditApplication[ID comparable] interface {
	app.IDescriptorProvider
	appaudit.IAuditReader[ID]
	MaxPageSize() int
}
