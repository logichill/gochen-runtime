package repo

import (
	"fmt"
	"sort"
	"strings"

	"gochen/app/query"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

// applyAdvancedFilters 应用受控高级过滤条件。
func (r *Repo[T, ID]) applyAdvancedFilters(quer *queryBuilder, advanced query.AdvancedFilters) (*queryBuilder, error) {
	if len(advanced.Or) > 0 {
		expr, args, err := buildOrCondition(advanced.Or, quer.resolveQuotedAllowedColumn)
		if err != nil {
			return nil, err
		}
		if expr != "" {
			quer = quer.Where(expr, args...)
		}
	}
	if advanced.DateRange != nil {
		column, ok := quer.resolveQuotedAllowedColumn("created_at")
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput, "date range uses hard-coded field created_at, but it is not allowed").
				WithContext("field", "created_at")
		}
		if advanced.DateRange.Start != "" {
			quer = quer.Where(column+" >= ?", advanced.DateRange.Start)
		}
		if advanced.DateRange.End != "" {
			quer = quer.Where(column+" <= ?", advanced.DateRange.End)
		}
	}
	return quer, nil
}

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

// buildOrCondition 构造OrCondition。
func buildOrCondition(conditions []query.OrCondition, resolveColumn func(string) (string, bool)) (string, []any, error) {
	var exprs []string
	var args []any
	for _, condition := range conditions {
		if len(condition) == 0 {
			continue
		}
		var inner []string
		var innerArgs []any
		keys := make([]string, 0, len(condition))
		for key := range condition {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, rawKey := range keys {
			value := condition[rawKey]
			key := strings.TrimSpace(rawKey)
			if key == "" {
				return "", nil, errors.NewCode(errors.InvalidInput, "advanced filter field is required")
			}
			column := key
			if resolveColumn != nil {
				resolved, ok := resolveColumn(key)
				if !ok {
					return "", nil, errors.NewCode(errors.InvalidInput, "invalid advanced filter field").
						WithContext("field", key)
				}
				column = resolved
			}
			queryValue := query.StringValue(value)
			if err := validateQueryValue(key, queryValue); err != nil {
				return "", nil, err
			}
			inner = append(inner, fmt.Sprintf("%s = ?", column))
			innerArgs = append(innerArgs, queryValue.Any())
		}
		if len(inner) > 0 {
			exprs = append(exprs, "("+strings.Join(inner, " AND ")+")")
			args = append(args, innerArgs...)
		}
	}
	return strings.Join(exprs, " OR "), args, nil
}

// maxBindParameters 返回本仓储可用的占位参数预算。
//
// 向底层数据库探测（db.IBindParameterLimitProvider）；未实现即用默认值。
// IN 列表与多值 INSERT 受同一个约束，因此两处共用这一个来源。
// 批量上限默认 1000，正常批量根本切不动；分片是为了让配置了更大批量上限
// 或更低占位上限的调用方不至于撞上运行期报错，而不是常态开销。
func (r *Repo[T, ID]) maxBindParameters() int {
	if r == nil || r.orm == nil {
		return dialect.DefaultMaxBindParameters
	}
	if provider, ok := r.orm.Database().(db.IBindParameterLimitProvider); ok {
		return dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
	}
	return dialect.DefaultMaxBindParameters
}

// chunkSlice 把列表切成不超过 size 的分片，供逐片下发语句
// （IN 条件的 ID 列表、多值 INSERT 的行列表等）。
//
// 分片只改变语句条数，不改变原子性：所有分片都在调用方已开启的同一事务内执行。
func chunkSlice[E any](items []E, size int) [][]E {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 || len(items) <= size {
		return [][]E{items}
	}
	chunks := make([][]E, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[start:end])
	}
	return chunks
}
