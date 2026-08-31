package quick

import (
	"strings"

	"gochen-runtime/db/orm/repo"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/errors"
)

// IsolatedRepo 显式开启 L2 租户隔离仓储。
//
// 默认隔离列名通过三层约定解析（缺省为 "tenant_id"），取值来源自 contextx.TenantID。
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

// ScopedRepo 显式开启 L3 数据范围与乐观锁仓储。
//
// 默认注入 WithResourceKind(kind)、WithRequireScope() 并通过三层约定解析范围列与版本列。
func ScopedRepo[T domain.IEntity[ID], ID comparable](
	ormEngine orm.IOrm,
	table string,
	resourceKind string,
	opts ...repo.Option[T, ID],
) (*repo.Repo[T, ID], error) {
	resourceKind = strings.TrimSpace(resourceKind)
	if resourceKind == "" {
		return nil, errors.NewCode(errors.InvalidInput, "resource kind cannot be empty")
	}
	allOpts := append([]repo.Option[T, ID]{
		repo.WithResourceKind[T, ID](resourceKind),
		repo.WithRequireScope[T, ID](),
		repo.WithScope[T, ID](repo.ScopeCols{}),
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
