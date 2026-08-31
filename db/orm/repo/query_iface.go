package repo

import (
	"context"
	"strings"

	"gochen/app/query"
	"gochen/errors"
)

func applyQueryFilters(quer *queryBuilder, opts query.QueryOptions) (*queryBuilder, error) {
	if !opts.Filters.IsZero() {
		return quer.withQueryFilters(opts.Filters)
	}
	return quer, nil
}

func (r *Repo[T, ID]) applyAdvancedQueryFilters(quer *queryBuilder, opts query.QueryOptions) (*queryBuilder, error) {
	if !opts.Advanced.IsZero() {
		return r.applyAdvancedFilters(quer, opts.Advanced)
	}
	return quer, nil
}

// applySelectFields 应用Select字段集合。
func (r *Repo[T, ID]) applySelectFields(quer *queryBuilder, fields []string) (*queryBuilder, error) {
	if len(fields) == 0 {
		return quer, nil
	}
	allowed := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if column, ok := quer.resolveAllowedColumn(f); ok {
			allowed = append(allowed, column)
			continue
		}
		return nil, errors.NewCode(errors.InvalidInput, "invalid select field").
			WithContext("field", f)
	}
	if len(allowed) == 0 {
		return quer, nil
	}
	return quer.Select(allowed...), nil
}

// Query 从存储中查询对象。
//
// 说明：
// - Query 执行通用查询（实现 app/query.IQueryableRepository）
func (r *Repo[T, ID]) Query(ctx context.Context, opts query.QueryOptions) ([]T, error) {
	var entities []T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if quer, err = r.applySelectFields(quer, opts.Fields); err != nil {
		return nil, err
	}
	if quer, err = applyQueryFilters(quer, opts); err != nil {
		return nil, err
	}
	if quer, err = r.applyAdvancedQueryFilters(quer, opts); err != nil {
		return nil, err
	}
	if quer, err = r.applySorts(quer, opts.Sorts); err != nil {
		return nil, err
	}
	if opts.Offset > 0 {
		quer = quer.Offset(opts.Offset)
	}
	if opts.Limit > 0 {
		quer = quer.Limit(opts.Limit)
	}
	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to execute query")
	}
	return entities, nil
}

// QueryOne 从存储中查询对象。
//
// 说明：
// - QueryOne 查询单条记录（实现 app/query.IQueryableRepository）
func (r *Repo[T, ID]) QueryOne(ctx context.Context, opts query.QueryOptions) (T, error) {
	var entity T
	var zero T
	quer, err := r.readQuery(ctx)
	if err != nil {
		return zero, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if quer, err = r.applySelectFields(quer, opts.Fields); err != nil {
		return zero, err
	}
	if quer, err = applyQueryFilters(quer, opts); err != nil {
		return zero, err
	}
	if quer, err = r.applyAdvancedQueryFilters(quer, opts); err != nil {
		return zero, err
	}
	if quer, err = r.applySorts(quer, opts.Sorts); err != nil {
		return zero, err
	}
	err = quer.First(&entity)
	if err != nil {
		if errors.Is(err, errors.NotFound) {
			return zero, errors.NewCode(errors.NotFound, "record not found")
		}
		return zero, errors.Wrap(err, errors.Database, "failed to query single record")
	}
	return entity, nil
}

// QueryCount 从存储中查询对象。
//
// 说明：
// - QueryCount 查询数量（实现 app/query.IQueryableRepository）
func (r *Repo[T, ID]) QueryCount(ctx context.Context, opts query.QueryOptions) (int64, error) {
	quer, err := r.readQuery(ctx)
	if err != nil {
		return 0, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if quer, err = applyQueryFilters(quer, opts); err != nil {
		return 0, err
	}
	if quer, err = r.applyAdvancedQueryFilters(quer, opts); err != nil {
		return 0, err
	}
	count, err := quer.Count()
	if err != nil {
		return 0, errors.Wrap(err, errors.Database, "failed to count records")
	}
	return count, nil
}
