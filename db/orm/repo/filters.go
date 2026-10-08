package repo

import (
	"fmt"
	"sort"
	"strings"

	"gochen/app/query"
	"gochen/errors"
)

var likePatternEscaper = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

func (quer *queryBuilder) withFilters(filters []query.Filter) (*queryBuilder, error) {
	for _, f := range filters {
		field := strings.TrimSpace(f.Field)
		if field == "" {
			return nil, errors.NewCode(errors.InvalidInput, "query filter field is required")
		}
		column, ok := quer.resolveQuotedAllowedColumn(field)
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput, "invalid query filter field").
				WithContext("field", field)
		}
		if !f.Op.IsValid() {
			return nil, errors.NewCode(errors.InvalidInput, "invalid query filter operator").
				WithContext("field", field).
				WithContext("op", string(f.Op))
		}

		var err error
		quer, err = applyFilterOp(quer, column, f.Op, f.Value, stringArgs(f.Values))
		if err != nil {
			return nil, err
		}
	}
	return quer, nil
}

func (quer *queryBuilder) withQueryFilters(filters query.QueryFilters) (*queryBuilder, error) {
	if filters.IsZero() {
		return quer, nil
	}
	fields := make([]string, 0, len(filters))
	normalized := make(map[string][]query.QueryExpr, len(filters))
	for field, exprs := range filters {
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, errors.NewCode(errors.InvalidInput, "query filter field is required")
		}
		if len(exprs) == 0 {
			continue
		}
		if _, exists := normalized[field]; !exists {
			fields = append(fields, field)
		}
		normalized[field] = append(normalized[field], exprs...)
	}
	sort.Strings(fields)
	for _, field := range fields {
		column, ok := quer.resolveQuotedAllowedColumn(field)
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput, "invalid query filter field").
				WithContext("field", field)
		}
		for _, expr := range normalized[field] {
			if !expr.Op.IsValid() {
				return nil, errors.NewCode(errors.InvalidInput, "invalid query filter operator").
					WithContext("field", field).
					WithContext("op", string(expr.Op))
			}
			if err := validateQueryExpr(field, expr); err != nil {
				return nil, err
			}
			var err error
			quer, err = applyFilterOp(quer, column, expr.Op, expr.Value.Any(), queryExprArgs(expr.Values))
			if err != nil {
				return nil, err
			}
		}
	}
	return quer, nil
}

func applyFilterOp(quer *queryBuilder, field string, op query.FilterOp, value any, values []any) (*queryBuilder, error) {
	switch op {
	case query.FilterOpEq:
		return quer.Where(field+" = ?", value), nil
	case query.FilterOpNe:
		return quer.Where(field+" != ?", value), nil
	case query.FilterOpLike:
		text, ok := value.(string)
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput, "query filter like value must be string").
				WithContext("field", field)
		}
		suffix, err := quer.likeContainsExprSuffix()
		if err != nil {
			return nil, err
		}
		return quer.Where(field+suffix, likeContainsPattern(text)), nil
	case query.FilterOpGt:
		return quer.Where(field+" > ?", value), nil
	case query.FilterOpGte:
		return quer.Where(field+" >= ?", value), nil
	case query.FilterOpLt:
		return quer.Where(field+" < ?", value), nil
	case query.FilterOpLte:
		return quer.Where(field+" <= ?", value), nil
	case query.FilterOpIn:
		if len(values) == 0 {
			return nil, errors.NewCode(errors.InvalidInput, "query filter values are required").
				WithContext("field", field).
				WithContext("op", string(op))
		}
		return quer.Where(field+" IN ?", values), nil
	case query.FilterOpNotIn:
		if len(values) == 0 {
			return nil, errors.NewCode(errors.InvalidInput, "query filter values are required").
				WithContext("field", field).
				WithContext("op", string(op))
		}
		return quer.Where(field+" NOT IN ?", values), nil
	case query.FilterOpIsNull:
		return quer.Where(field + " IS NULL"), nil
	case query.FilterOpNotNull:
		return quer.Where(field + " IS NOT NULL"), nil
	default:
		return nil, errors.NewCode(errors.InvalidInput, "invalid query filter operator").
			WithContext("field", field).
			WithContext("op", string(op))
	}
}

func (quer *queryBuilder) likeContainsExprSuffix() (string, error) {
	if quer == nil || quer.model == nil {
		return "", errors.NewCode(errors.InvalidInput, "ORM model dialect is required")
	}
	d := quer.model.Dialect()
	if d == nil {
		return "", errors.NewCode(errors.InvalidInput, "ORM model dialect is required")
	}
	return " LIKE ?" + d.LikeEscapeClause(), nil
}

func likeContainsPattern(value string) string {
	return "%" + escapeLikePattern(value) + "%"
}

func escapeLikePattern(value string) string {
	return likePatternEscaper.Replace(value)
}

func queryExprArgs(values []query.QueryValue) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value.Any())
	}
	return out
}

func stringArgs(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func validateQueryValues(field string, values []query.QueryValue) error {
	for _, value := range values {
		if err := validateQueryValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateQueryExpr(field string, expr query.QueryExpr) error {
	switch expr.Op {
	case query.FilterOpEq, query.FilterOpNe,
		query.FilterOpGt, query.FilterOpGte, query.FilterOpLt, query.FilterOpLte:
		return validateQueryValue(field, expr.Value)
	case query.FilterOpLike:
		if err := validateQueryValue(field, expr.Value); err != nil {
			return err
		}
		if expr.Value.Type != query.FieldTypeString && expr.Value.Type != query.FieldTypeEnum {
			return errors.NewCode(errors.InvalidInput, "query filter like value must be string").
				WithContext("field", field).
				WithContext("type", string(expr.Value.Type))
		}
		return nil
	case query.FilterOpIn, query.FilterOpNotIn:
		return validateQueryValues(field, expr.Values)
	default:
		return nil
	}
}

func validateQueryValue(field string, value query.QueryValue) error {
	if !value.Type.IsValid() {
		if value.Type != "" {
			return errors.NewCode(errors.InvalidInput, "invalid query filter value type").
				WithContext("field", field).
				WithContext("type", string(value.Type))
		}
		return errors.NewCode(errors.InvalidInput, "query filter value type is required").
			WithContext("field", field)
	}
	return nil
}

// applyAdvancedFilters 应用受控高级过滤条件。
func (r *Repo[T, ID]) applyAdvancedFilters(quer *queryBuilder, advanced query.AdvancedFilters) (*queryBuilder, error) {
	if len(advanced.Or) > 0 {
		expr, args, err := buildOrCondition(advanced.Or, quer.resolveQuotedAllowedColumn)
		if err != nil {
			return nil, err
		}
		if expr != "" {
			quer = quer.Where("("+expr+")", args...)
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
