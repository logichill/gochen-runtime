package repo

import (
	"context"

	"gochen/db/orm"
	"gochen/domain/spec"
	"gochen/errors"
)

// ScopedQuery 暴露从统一 DataScope 入口构造出的查询能力，供下游 repo
// 在保留 tenant/scope 边界的前提下补充 preload/join 等领域查询形状。
type ScopedQuery struct {
	builder *queryBuilder
	err     error
}

func newScopedQuery(builder *queryBuilder) *ScopedQuery {
	return &ScopedQuery{builder: builder}
}

func (quer *ScopedQuery) hasError() bool {
	return quer == nil || quer.err != nil
}

// IUnsafeSelectExprCapability 是使用原始 SELECT 表达式的显式能力门。
//
// 说明：
// - 仅当表达式完全由可信常量或框架生成，且不拼接用户输入时才应传入；
// - 下游需要在调用点显式使用 `repo.AllowUnsafeSelectExpr()`，便于代码审查定位 unsafe SQL 面。
type IUnsafeSelectExprCapability interface {
	allowUnsafeSelectExpr()
}

type unsafeSelectExprCapability struct{}

func (unsafeSelectExprCapability) allowUnsafeSelectExpr() {}

// AllowUnsafeSelectExpr 返回使用 SelectExprUnsafe 所需的显式 opt-in token。
func AllowUnsafeSelectExpr() IUnsafeSelectExprCapability {
	return unsafeSelectExprCapability{}
}

func (quer *ScopedQuery) Where(expr string, args ...any) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Where(expr, args...)
	return quer
}

func (quer *ScopedQuery) Join(join orm.Join) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Join(join)
	return quer
}

func (quer *ScopedQuery) Preload(relations ...string) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Preload(relations...)
	return quer
}

func (quer *ScopedQuery) Order(column string, desc bool) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Order(column, desc)
	return quer
}

func (quer *ScopedQuery) Limit(limit int) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Limit(limit)
	return quer
}

func (quer *ScopedQuery) Offset(offset int) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Offset(offset)
	return quer
}

func (quer *ScopedQuery) Select(columns ...string) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.Select(columns...)
	return quer
}

// SelectExprUnsafe 指定原始 SELECT 表达式。
//
// Unsafe 契约：
// - expressions 会原样传给 ORM 的 SELECT raw 通道，不做字段白名单校验；
// - 调用方必须传入 AllowUnsafeSelectExpr()，并保证表达式不包含任何用户输入。
func (quer *ScopedQuery) SelectExprUnsafe(capability IUnsafeSelectExprCapability, expressions ...string) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	if capability == nil {
		quer.err = errors.NewCode(errors.InvalidInput, "SelectExprUnsafe requires explicit unsafe select capability")
		return quer
	}
	quer.builder.SelectExprUnsafe(expressions...)
	return quer
}

func (quer *ScopedQuery) GroupBy(columns ...string) *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.GroupBy(columns...)
	return quer
}

func (quer *ScopedQuery) ForUpdate() *ScopedQuery {
	if quer.hasError() {
		return quer
	}
	quer.builder.ForUpdate()
	return quer
}

func (quer *ScopedQuery) First(dest any) error {
	if quer.err != nil {
		return quer.err
	}
	return quer.builder.First(dest)
}

func (quer *ScopedQuery) Find(dest any) error {
	if quer.err != nil {
		return quer.err
	}
	return quer.builder.Find(dest)
}

func (quer *ScopedQuery) Count() (int64, error) {
	if quer.err != nil {
		return 0, quer.err
	}
	return quer.builder.Count()
}

// ScopedQuery 创建一个默认已套用 DataScope 的查询入口。
func (r *Repo[T, ID]) ScopedQuery(ctx context.Context) (*ScopedQuery, error) {
	builder, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	query := newScopedQuery(builder)
	if r.softDelete {
		query.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	return query, nil
}

// ScopedQueryWithSpec 保留仓储隔离和软删条件，并在 JOIN、预加载、分页和统计前落实授权条件。
// 框架生成的列引用限定为本仓储主表，支持关联表具有相同列名的查询。
func (r *Repo[T, ID]) ScopedQueryWithSpec(ctx context.Context, condition spec.Spec) (*ScopedQuery, error) {
	qualified := *r
	resolver := *r.resolver
	resolver.qualifier = r.table
	qualified.resolver = &resolver
	builder, err := qualified.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	builder = qualified.withActive(builder)
	builder, err = qualified.withReadSpec(builder, condition)
	if err != nil {
		return nil, err
	}
	return newScopedQuery(builder), nil
}

// GetWith 在统一 DataScope/soft-delete 边界上追加额外的查询形状后按主键读取。
func (r *Repo[T, ID]) GetWith(ctx context.Context, id ID, configure func(*ScopedQuery)) (T, error) {
	return r.FindOneWith(ctx, func(quer *ScopedQuery) {
		quer.Where("id = ?", id)
		if configure != nil {
			configure(quer)
		}
	})
}

// FindOneWith 在统一 DataScope/soft-delete 边界上追加额外查询条件，读取单条记录。
func (r *Repo[T, ID]) FindOneWith(ctx context.Context, configure func(*ScopedQuery)) (T, error) {
	var zero T
	quer, err := r.ScopedQuery(ctx)
	if err != nil {
		return zero, err
	}
	if configure != nil {
		configure(quer)
	}
	var entity T
	if err := quer.First(&entity); err != nil {
		if errors.Is(err, errors.NotFound) {
			return zero, errors.NewCode(errors.NotFound, "record not found")
		}
		return zero, errors.Wrap(err, errors.Database, "failed to query record")
	}
	return entity, nil
}
