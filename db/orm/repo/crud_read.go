package repo

import (
	"context"

	"gochen/errors"
)

// Get 按主键读取一条未软删除记录。
func (r *Repo[T, ID]) Get(ctx context.Context, id ID) (T, error) {
	return r.get(ctx, id, false)
}

// GetWithDeleted 按主键读取记录，并允许命中软删除数据。
func (r *Repo[T, ID]) GetWithDeleted(ctx context.Context, id ID) (T, error) {
	return r.get(ctx, id, true)
}

// get 统一处理按主键读取记录的公共逻辑。
func (r *Repo[T, ID]) get(ctx context.Context, id ID, includeSoftDeleted bool) (T, error) {
	var zero T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return zero, err
	}
	quer = quer.Where("id = ?", id)
	if r.softDelete && !includeSoftDeleted {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
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

func (r *Repo[T, ID]) List(ctx context.Context, offset, limit int) ([]T, error) {
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
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

func (r *Repo[T, ID]) ListDeleted(ctx context.Context, offset, limit int) ([]T, error) {
	if !r.softDelete {
		return []T{}, nil
	}
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NOT NULL")
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

// Count 统计当前仓储下未软删除记录的数量。
func (r *Repo[T, ID]) Count(ctx context.Context) (int64, error) {
	quer, err := r.readQuery(ctx)
	if err != nil {
		return 0, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	count, err := quer.Count()
	if err != nil {
		return 0, errors.Wrap(err, errors.Database, "failed to count records")
	}
	return count, nil
}

// Exists 判断指定主键对应的未软删除记录是否存在。
func (r *Repo[T, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	quer, err := r.readQuery(ctx)
	if err != nil {
		return false, err
	}
	quer = quer.Where("id = ?", id)
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	count, err := quer.Count()
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "failed to check record existence")
	}
	return count > 0, nil
}

func (r *Repo[T, ID]) ListAll(ctx context.Context) ([]T, error) {
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to list all records")
	}
	return entities, nil
}

// ListByIds 从存储中查询对象。
//
// 参数：
// - ids：对象/实体标识列表。
func (r *Repo[T, ID]) ListByIds(ctx context.Context, ids []ID) ([]T, error) {
	if len(ids) == 0 {
		return []T{}, nil
	}
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	quer = quer.Where("id IN ?", ids)
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to list records by IDs")
	}
	return entities, nil
}
