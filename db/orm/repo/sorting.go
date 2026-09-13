package repo

import (
	"strings"

	"gochen/app/query"
	"gochen/errors"
)

// applySorting 应用Sorting。
func (r *Repo[T, ID]) applySorting(quer *queryBuilder, request *query.PaginationOptions) (*queryBuilder, error) {
	if request == nil {
		return quer, nil
	}
	return r.applySorts(quer, request.Sorts)
}

func (r *Repo[T, ID]) applySorts(quer *queryBuilder, sorts []query.Sort) (*queryBuilder, error) {
	if len(sorts) == 0 {
		return quer, nil
	}
	for _, s := range sorts {
		field := strings.TrimSpace(s.Field)
		direction := s.Direction
		if field == "" {
			return nil, errors.NewCode(errors.InvalidInput, "sort field is required")
		}
		if !direction.IsValid() {
			return nil, errors.NewCode(errors.InvalidInput, "invalid sort direction").
				WithContext("field", field).
				WithContext("direction", string(direction))
		}
		column, ok := quer.resolveAllowedColumn(field)
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput, "invalid order by field").
				WithContext("field", field)
		}
		quer = quer.Order(column, direction == query.DESC)
	}
	return quer, nil
}
