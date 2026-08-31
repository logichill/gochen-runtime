package repo

import (
	"context"
	"math"

	"gochen/app/query"
	"gochen/errors"
)

const defaultMaxPageSize = 1000

// ListPage 从存储中查询对象。
func (r *Repo[T, ID]) ListPage(ctx context.Context, request *query.PaginationOptions) (*query.PagedResult[T], error) {
	var entities []T
	var total int64

	if request == nil {
		request = &query.PaginationOptions{}
	}
	maxPageSize := r.maxPageSize
	if maxPageSize <= 0 {
		maxPageSize = defaultMaxPageSize
	}
	if err := request.Validate(maxPageSize); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid pagination options")
	}

	quer, err := r.readQuery(ctx)
	if err != nil {
		return nil, err
	}
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if !request.Filters.IsZero() {
		quer, err = quer.withQueryFilters(request.Filters)
		if err != nil {
			return nil, err
		}
	}
	if !request.Advanced.IsZero() {
		quer, err = r.applyAdvancedFilters(quer, request.Advanced)
		if err != nil {
			return nil, err
		}
	}

	total, err = quer.Count()
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to count total records")
	}

	quer, err = r.applySorting(quer, request)
	if err != nil {
		return nil, err
	}
	if quer, err = r.applySelectFields(quer, request.Fields); err != nil {
		return nil, err
	}
	quer = quer.Offset(request.Offset()).Limit(request.Size)

	if err := quer.Find(&entities); err != nil {
		return nil, errors.Wrap(err, errors.Database, "failed to execute paginated query")
	}

	size := request.Size
	if size <= 0 {
		size = 1
	}
	totalPages := int(math.Ceil(float64(total) / float64(size)))
	hasNext := request.Page < totalPages
	hasPrev := request.Page > 1

	return &query.PagedResult[T]{
		Data:       entities,
		Total:      total,
		Page:       request.Page,
		Size:       size,
		TotalPages: totalPages,
		HasNext:    hasNext,
		HasPrev:    hasPrev,
	}, nil
}
