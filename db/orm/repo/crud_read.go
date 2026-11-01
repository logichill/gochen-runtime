package repo

import (
	"context"

	"gochen/domain/spec"
	"gochen/errors"
)

// Get 按主键读取一条未软删除记录，并按规约过滤。
func (r *Repo[T, ID]) Get(ctx context.Context, id ID, s spec.Spec) (T, error) {
	return r.get(ctx, id, s, false)
}

// GetWithDeleted 按主键读取记录，并允许命中软删除数据。
func (r *Repo[T, ID]) GetWithDeleted(ctx context.Context, id ID, s spec.Spec) (T, error) {
	return r.get(ctx, id, s, true)
}

// get 统一处理按主键读取记录的公共逻辑。
func (r *Repo[T, ID]) get(ctx context.Context, id ID, s spec.Spec, includeSoftDeleted bool) (T, error) {
	var zero T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return zero, err
	}
	quer = r.withID(quer, id)
	if !includeSoftDeleted {
		quer = r.withActive(quer)
	}
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return zero, err
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

// List 分页读取未软删除记录，并按规约过滤。
func (r *Repo[T, ID]) List(ctx context.Context, offset, limit int, s spec.Spec) ([]T, error) {
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	quer = r.withActive(quer)
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		quer = quer.Offset(offset)
	}
	if limit > 0 {
		quer = quer.Limit(limit)
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to list records")
	}
	return entities, nil
}

// ListDeleted 读取已软删记录，并按规约过滤。
func (r *Repo[T, ID]) ListDeleted(ctx context.Context, offset, limit int, s spec.Spec) ([]T, error) {
	if !r.softDelete {
		return []T{}, nil
	}
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	quer = r.withDeleted(quer)
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		quer = quer.Offset(offset)
	}
	if limit > 0 {
		quer = quer.Limit(limit)
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to list deleted records")
	}
	return entities, nil
}

// Count 统计当前仓储下满足规约的未软删除记录数量。
func (r *Repo[T, ID]) Count(ctx context.Context, s spec.Spec) (int64, error) {
	quer, err := r.readQuery(ctx)
	if err != nil {
		return 0, err
	}
	quer = r.withActive(quer)
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return 0, err
	}
	count, err := quer.Count()
	if err != nil {
		return 0, errors.Wrap(err, errors.Database, "failed to count records")
	}
	return count, nil
}

// Exists 判断指定主键对应的未软删除记录是否存在，并按规约过滤。
func (r *Repo[T, ID]) Exists(ctx context.Context, id ID, s spec.Spec) (bool, error) {
	quer, err := r.readQuery(ctx)
	if err != nil {
		return false, err
	}
	quer = r.withID(quer, id)
	quer = r.withActive(quer)
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return false, err
	}
	count, err := quer.Count()
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "failed to check record existence")
	}
	return count > 0, nil
}

// ListAll 读取全部未软删除记录，并按规约过滤。
func (r *Repo[T, ID]) ListAll(ctx context.Context, s spec.Spec) ([]T, error) {
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	quer = r.withActive(quer)
	quer, err = r.withReadSpec(quer, s)
	if err != nil {
		return nil, err
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to list all records")
	}
	return entities, nil
}

// ListByIds 按主键集合读取未软删除记录，并按规约过滤。
//
// 说明：
//   - 结果顺序不做保证，与数据库对 `id IN (...)` 的返回顺序一致；
//   - ID 列表按占位参数预算分片下发，避免超出驱动的变量上限把整次查询打成
//     运行期错误；重复 ID 在被分片时会先折叠，与单条 IN 查询的语义保持一致。
func (r *Repo[T, ID]) ListByIds(ctx context.Context, ids []ID, s spec.Spec) ([]T, error) {
	if len(ids) == 0 {
		return []T{}, nil
	}
	base, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	base = r.withActive(base)
	base, err = r.withReadSpec(base, s)
	if err != nil {
		return nil, err
	}
	budget, err := r.idBatchBudget(base, 0)
	if err != nil {
		return nil, err
	}
	if len(ids) > budget {
		// 分片后同一 ID 落在不同分片会各返回一行，而单条 IN 只返回一行；
		// 仅在真正需要分片时折叠，常态路径不做额外分配。
		ids = dedupeSlice(ids)
	}
	var entities []T
	for _, chunk := range chunkSlice(ids, budget) {
		quer := base.clone()
		quer = quer.Where(r.resolver.quote(r.idColumn)+" IN ?", chunk)
		var chunkEntities []T
		if err := quer.Find(&chunkEntities); err != nil {
			return nil, errors.Wrap(err, errors.Database, "failed to list records by IDs")
		}
		entities = append(entities, chunkEntities...)
	}
	return entities, nil
}

func (r *Repo[T, ID]) withID(quer *queryBuilder, id ID) *queryBuilder {
	return quer.Where(r.resolver.quote(r.idColumn)+" = ?", id)
}

func (r *Repo[T, ID]) withActive(quer *queryBuilder) *queryBuilder {
	if !r.softDelete || r.softDeleteCols.DeletedAt == "" {
		return quer
	}
	return quer.Where(r.resolver.quote(r.softDeleteCols.DeletedAt) + " IS NULL")
}

func (r *Repo[T, ID]) withDeleted(quer *queryBuilder) *queryBuilder {
	if !r.softDelete || r.softDeleteCols.DeletedAt == "" {
		return quer
	}
	return quer.Where(r.resolver.quote(r.softDeleteCols.DeletedAt) + " IS NOT NULL")
}

func (r *Repo[T, ID]) withReadSpec(quer *queryBuilder, s spec.Spec) (*queryBuilder, error) {
	condition, err := r.specCondition(s)
	if err != nil {
		return nil, err
	}
	return applyCondition(quer, condition), nil
}
