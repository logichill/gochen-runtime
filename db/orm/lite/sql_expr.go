package lite

import (
	coredialect "gochen/db/dialect"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen-runtime/db/sql/sqlbuilder"

	"gochen/db/orm"
	"gochen/errors"
)

// buildTableExpr 执行对应操作。
func buildTableExpr(d coredialect.IDialect, base string, joins []orm.Join) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.NewCode(errors.InvalidInput, "table name cannot be empty")
	}
	if !safeident.IsSafeIdentifier(base) {
		return "", errors.NewCode(errors.InvalidInput, "unsafe table name").WithContext("table", base)
	}
	if len(joins) == 0 {
		return base, nil
	}
	var sb strings.Builder
	// JOIN 场景走 FromUnsafe，需要在此处按方言 quote，避免出现“SELECT 被 quote 但 FROM 未 quote”的不一致。
	sb.WriteString(d.QuoteIdentifier(base))
	for _, j := range joins {
		switch j.Type {
		case orm.JoinInner, orm.JoinLeft, orm.JoinRight:
		default:
			return "", errors.NewCode(errors.InvalidInput, "invalid join type").WithContext("join_type", string(j.Type))
		}
		table := strings.TrimSpace(j.Table)
		if table == "" {
			return "", errors.NewCode(errors.InvalidInput, "join table cannot be empty")
		}
		if !safeident.IsSafeIdentifier(table) {
			return "", errors.NewCode(errors.InvalidInput, "unsafe join table").WithContext("table", table)
		}
		alias := strings.TrimSpace(j.Alias)
		if alias != "" {
			if strings.Contains(alias, ".") {
				return "", errors.NewCode(errors.InvalidInput, "join alias cannot contain dot").WithContext("alias", alias)
			}
			if !safeident.IsSafeIdentifier(alias) {
				return "", errors.NewCode(errors.InvalidInput, "unsafe join alias").WithContext("alias", alias)
			}
		}
		if len(j.On) == 0 {
			return "", errors.NewCode(errors.InvalidInput, "join on conditions cannot be empty").
				WithContext("table", table)
		}
		sb.WriteRune(' ')
		sb.WriteString(string(j.Type))
		sb.WriteString(" JOIN ")
		sb.WriteString(d.QuoteIdentifier(table))
		if alias != "" {
			sb.WriteRune(' ')
			sb.WriteString(d.QuoteIdentifier(alias))
		}
		sb.WriteString(" ON ")
		for i, on := range j.On {
			left := strings.TrimSpace(on.Left)
			right := strings.TrimSpace(on.Right)
			if left == "" || right == "" {
				return "", errors.NewCode(errors.InvalidInput, "join on identifiers cannot be empty").
					WithContext("table", table)
			}
			if !safeident.IsSafeIdentifier(left) {
				return "", errors.NewCode(errors.InvalidInput, "unsafe join on left identifier").WithContext("identifier", left)
			}
			if !safeident.IsSafeIdentifier(right) {
				return "", errors.NewCode(errors.InvalidInput, "unsafe join on right identifier").WithContext("identifier", right)
			}
			if i > 0 {
				sb.WriteString(" AND ")
			}
			sb.WriteString(d.QuoteIdentifier(left))
			sb.WriteString(" = ")
			sb.WriteString(d.QuoteIdentifier(right))
		}
	}
	return sb.String(), nil
}

// buildOrderBy 构造排序。
func buildOrderBy(orders []orm.OrderBy) ([]sqlbuilder.Order, error) {
	if len(orders) == 0 {
		return nil, nil
	}
	result := make([]sqlbuilder.Order, 0, len(orders))
	for _, o := range orders {
		if o.Column == "" {
			continue
		}
		if !safeident.IsSafeIdentifier(o.Column) {
			return nil, errors.NewCode(errors.InvalidInput, "invalid order by column").WithContext("column", o.Column)
		}
		result = append(result, sqlbuilder.Order{Column: o.Column, Desc: o.Desc})
	}
	return result, nil
}

// newSelectBuilder 根据查询选项构造 SELECT 构建器。
//
// 安全约定：纯安全列查询走带标识符校验的 Select。混用 qo.SelectRaw 时，
// qo.Select 中的安全列先各自经 IsSafeIdentifier 校验并按方言 quote，再与 raw
// 表达式合并走 SelectExprUnsafe；这样安全列不会借 raw 通道绕过校验扩大注入面，
// 而调用方显式声明的 raw 表达式仍按原样透传。
func newSelectBuilder(sql sqlbuilder.ISQL, d coredialect.IDialect, qo orm.QueryOptions) (sqlbuilder.ISelectBuilder, error) {
	if len(qo.SelectRaw) > 0 {
		columns := make([]string, 0, len(qo.Select)+len(qo.SelectRaw))
		for _, col := range qo.Select {
			c := strings.TrimSpace(col)
			if c == "" {
				continue
			}
			if c == "*" {
				columns = append(columns, c)
				continue
			}
			if !safeident.IsSafeIdentifier(c) {
				return nil, errors.NewCode(errors.InvalidInput, "unsafe select column").WithContext("column", c)
			}
			columns = append(columns, d.QuoteIdentifier(c))
		}
		columns = append(columns, qo.SelectRaw...)
		if len(columns) == 0 {
			columns = []string{"*"}
		}
		return sql.SelectExprUnsafe(columns...), nil
	}
	columns := qo.Select
	if len(columns) == 0 {
		columns = []string{"*"}
	}
	return sql.Select(columns...), nil
}
