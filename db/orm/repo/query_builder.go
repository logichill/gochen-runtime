package repo

import (
	"context"
	"database/sql"

	"gochen/db/orm"
	"gochen/errors"
)

type queryBuilder struct {
	ctx   context.Context
	model orm.IModel
	opts  []orm.QueryOption
}

// newQueryBuilder 创建绑定了上下文与模型入口的查询构建器。
func newQueryBuilder(model orm.IModel, ctx context.Context) *queryBuilder {
	return &queryBuilder{
		ctx:   ctx,
		model: model,
	}
}

func (quer *queryBuilder) clone() *queryBuilder {
	return &queryBuilder{ctx: quer.ctx, model: quer.model, opts: append([]orm.QueryOption(nil), quer.opts...)}
}

// Where 追加一段带参数的 WHERE 条件。
func (quer *queryBuilder) Where(expr string, args ...any) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithWhere(expr, args...))
	return quer
}

// Join 追加一个结构化 JOIN 子句。
func (quer *queryBuilder) Join(join orm.Join) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithJoin(join))
	return quer
}

// Preload 追加预加载关系。
func (quer *queryBuilder) Preload(relations ...string) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithPreload(relations...))
	return quer
}

// Order 追加一条排序规则。
func (quer *queryBuilder) Order(column string, desc bool) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithOrderBy(column, desc))
	return quer
}

// Limit 设置最大返回条数。
func (quer *queryBuilder) Limit(limit int) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithLimit(limit))
	return quer
}

// Offset 设置查询偏移量。
func (quer *queryBuilder) Offset(offset int) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithOffset(offset))
	return quer
}

// Select 指定需要返回的列集合。
func (quer *queryBuilder) Select(columns ...string) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithSelect(columns...))
	return quer
}

// SelectExprUnsafe 指定原始 SELECT 表达式；调用方必须确保表达式不含用户输入。
func (quer *queryBuilder) SelectExprUnsafe(expressions ...string) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithSelectExprUnsafe(expressions...))
	return quer
}

// GroupBy 设置 GROUP BY 字段列表。
func (quer *queryBuilder) GroupBy(columns ...string) *queryBuilder {
	quer.opts = append(quer.opts, orm.WithGroupBy(columns...))
	return quer
}

// ForUpdate 为查询追加行级锁语义。
func (quer *queryBuilder) ForUpdate() *queryBuilder {
	quer.opts = append(quer.opts, orm.WithForUpdate())
	return quer
}

// First 查询第一条记录并写入目标对象。
func (quer *queryBuilder) First(dest any) error {
	return quer.model.First(quer.ctx, dest, quer.opts...)
}

// Find 执行查询并把结果集合写入目标对象。
func (quer *queryBuilder) Find(dest any) error {
	return quer.model.Find(quer.ctx, dest, quer.opts...)
}

func (quer *queryBuilder) Count() (int64, error) {
	return quer.model.Count(quer.ctx, quer.opts...)
}

// Create 通过底层模型入口创建记录。
func (quer *queryBuilder) Create(values ...any) error {
	return quer.model.Create(quer.ctx, values...)
}

// UpdateValues 按当前查询条件执行字段更新。
func (quer *queryBuilder) UpdateValues(values map[string]any) error {
	return quer.model.UpdateValues(quer.ctx, values, quer.opts...)
}

// UpdateValuesWithResult 更新对象并返回受影响行数。
func (quer *queryBuilder) UpdateValuesWithResult(values map[string]any) (sql.Result, error) {
	if m, ok := quer.model.(orm.IModelWithResult); ok {
		return m.UpdateValuesWithResult(quer.ctx, values, quer.opts...)
	}
	return nil, errors.NewCode(errors.Unsupported, "orm: model does not support result")
}

// Save 按底层模型语义保存实体。
func (quer *queryBuilder) Save(entity any) error {
	return quer.model.Save(quer.ctx, entity, quer.opts...)
}

// SaveWithResult 保存实体，并在支持时返回底层 sql.Result。
func (quer *queryBuilder) SaveWithResult(entity any) (sql.Result, error) {
	if m, ok := quer.model.(orm.IModelWithResult); ok {
		return m.SaveWithResult(quer.ctx, entity, quer.opts...)
	}
	return nil, errors.NewCode(errors.Unsupported, "orm: model does not support result")
}

// Delete 按当前查询条件删除记录。
func (quer *queryBuilder) Delete() error {
	return quer.model.Delete(quer.ctx, quer.opts...)
}

// DeleteWithResult 删除数据并返回受影响行数。
func (quer *queryBuilder) DeleteWithResult() (sql.Result, error) {
	if m, ok := quer.model.(orm.IModelWithResult); ok {
		return m.DeleteWithResult(quer.ctx, quer.opts...)
	}
	return nil, errors.NewCode(errors.Unsupported, "orm: model does not support result")
}
