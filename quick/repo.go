package quick

import (
	"gochen-runtime/db/orm/repo"
	"gochen/db/orm"
	"gochen/domain"
)

// Repo 创建带默认选项的 ORM 仓储。
func Repo[T domain.IEntity[ID], ID comparable](
	ormEngine orm.IOrm,
	table string,
	opts ...repo.Option[T, ID],
) (*repo.Repo[T, ID], error) {
	return repo.NewRepo[T, ID](ormEngine, table, opts...)
}

// IsolatedRepo 显式开启租户隔离仓储。
//
// 默认隔离列名通过命名约定解析（缺省为 "tenant_id"），取值来源自 contextx.TenantID。
func IsolatedRepo[T domain.IEntity[ID], ID comparable](
	ormEngine orm.IOrm,
	table string,
	opts ...repo.Option[T, ID],
) (*repo.Repo[T, ID], error) {
	allOpts := append([]repo.Option[T, ID]{
		repo.WithIsolation[T, ID](repo.IsolationCols{}),
	}, opts...)
	return repo.NewRepo[T, ID](ormEngine, table, allOpts...)
}

// AuditedRepo 显式指定审计轨迹与软删除仓储。
//
// 适用于实体未组合标准 domain/audited 类型，但数据表具有软删除列的定制场景。
func AuditedRepo[T domain.IEntity[ID], ID comparable](
	ormEngine orm.IOrm,
	table string,
	opts ...repo.Option[T, ID],
) (*repo.Repo[T, ID], error) {
	allOpts := append([]repo.Option[T, ID]{
		repo.WithAudited[T, ID](),
	}, opts...)
	return repo.NewRepo[T, ID](ormEngine, table, allOpts...)
}
