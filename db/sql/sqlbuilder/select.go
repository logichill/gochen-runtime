package sqlbuilder

import (
	"context"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

type selectBuilder struct {
	db      db.IDatabase
	dialect dialect.IDialect

	cols          []string
	colsUnsafe    bool
	table         string
	tableUnsafe   bool
	where         []string
	args          []any
	groupBy       []string
	orderBy       []Order
	orderByUnsafe string
	limit         int
	offset        int
	locking       string

	err error
}

// setErr 记录首个构建错误，后续错误会被忽略。
func (b *selectBuilder) setErr(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// From 设置查询表名，并校验标识符安全性。
func (b *selectBuilder) From(table string) ISelectBuilder {
	if b.err != nil {
		return b
	}
	table = strings.TrimSpace(table)
	if table == "" {
		b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: From cannot be empty"))
		return b
	}
	if !safeident.IsSafeIdentifier(table) {
		b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: unsafe from table").
			WithContext("table", table))
		return b
	}
	b.table = table
	b.tableUnsafe = false
	return b
}

// FromUnsafe 设置 FROM 表达式（不做安全校验）。
func (b *selectBuilder) FromUnsafe(expr string) ISelectBuilder {
	if b.err != nil {
		return b
	}
	expr = strings.TrimSpace(expr)
	if expr == "" {
		b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: FromUnsafe cannot be empty"))
		return b
	}
	b.table = expr
	b.tableUnsafe = true
	return b
}

// Where 追加一段 WHERE 条件，并展开切片型占位参数。
func (b *selectBuilder) Where(cond string, args ...any) ISelectBuilder {
	if b.err != nil {
		return b
	}
	if cond != "" {
		expanded, flat, err := expandPlaceholders(cond, args, b.dialect)
		if err != nil {
			b.setErr(err)
			return b
		}
		b.where = append(b.where, expanded)
		b.args = append(b.args, flat...)
	}
	return b
}

// And 以 AND 语义追加条件。
func (b *selectBuilder) And(cond string, args ...any) ISelectBuilder {
	return b.Where(cond, args...)
}

// Or 把新条件与上一段 WHERE 子句用 OR 连接。
func (b *selectBuilder) Or(cond string, args ...any) ISelectBuilder {
	if b.err != nil {
		return b
	}
	if cond == "" {
		return b
	}
	expandedCond, flat, err := expandPlaceholders(cond, args, b.dialect)
	if err != nil {
		b.setErr(err)
		return b
	}
	if len(b.where) == 0 {
		b.where = append(b.where, expandedCond)
		b.args = append(b.args, flat...)
		return b
	}
	last := b.where[len(b.where)-1]
	b.where[len(b.where)-1] = "(" + last + " OR " + expandedCond + ")"
	b.args = append(b.args, flat...)
	return b
}

// GroupBy 设置 GROUP BY 列，并校验列名安全性。
func (b *selectBuilder) GroupBy(cols ...string) ISelectBuilder {
	if b.err != nil {
		return b
	}
	for _, col := range cols {
		if col == "" {
			continue
		}
		if !safeident.IsSafeIdentifier(col) {
			b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: unsafe group by column").
				WithContext("column", col))
			return b
		}
		b.groupBy = append(b.groupBy, col)
	}
	return b
}

// OrderBy 追加结构化排序规则。
func (b *selectBuilder) OrderBy(orders ...Order) ISelectBuilder {
	if b.err != nil {
		return b
	}
	for _, o := range orders {
		if o.Column == "" {
			continue
		}
		if !safeident.IsSafeIdentifier(o.Column) {
			b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: unsafe order by column").
				WithContext("column", o.Column))
			return b
		}
		b.orderBy = append(b.orderBy, o)
	}
	return b
}

// OrderByUnsafe 设置排序表达式（不做安全校验）。
func (b *selectBuilder) OrderByUnsafe(expr string) ISelectBuilder {
	if expr != "" {
		b.orderByUnsafe = expr
	}
	return b
}

// Limit 设置最大返回条数。
func (b *selectBuilder) Limit(n int) ISelectBuilder {
	if b.err != nil {
		return b
	}
	if n < 0 {
		b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: negative limit").WithContext("limit", n))
		return b
	}
	b.limit = n
	return b
}

// Offset 设置分页偏移量。
func (b *selectBuilder) Offset(n int) ISelectBuilder {
	if b.err != nil {
		return b
	}
	if n < 0 {
		b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: negative offset").WithContext("offset", n))
		return b
	}
	b.offset = n
	return b
}

// ForUpdate 为支持的方言启用 `FOR UPDATE` 行级锁。
func (b *selectBuilder) ForUpdate() ISelectBuilder {
	if b.err != nil {
		return b
	}
	locking, err := b.dialect.SelectLockClause(false)
	if err != nil {
		b.setErr(err)
		return b
	}
	b.locking = locking
	return b
}

// SkipLocked 为支持的方言启用 `SKIP LOCKED`。
func (b *selectBuilder) SkipLocked() ISelectBuilder {
	if b.err != nil {
		return b
	}
	locking, err := b.dialect.SelectLockClause(true)
	if err != nil {
		b.setErr(err)
		return b
	}
	b.locking = locking
	return b
}

// Build 生成最终 SQL 文本与参数列表。
func (b *selectBuilder) Build() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	if b.table == "" {
		return "", nil, errors.NewCode(errors.InvalidInput, "selectBuilder: From is required")
	}

	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(b.buildSelectColumns())
	if b.err != nil {
		return "", nil, b.err
	}
	sb.WriteString(" FROM ")
	sb.WriteString(b.buildTableName())

	// 使用局部 args 副本，避免在多次 Build 调用之间污染 builder 状态。
	args := make([]any, 0, len(b.args)+2)
	args = append(args, b.args...)

	if len(b.where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(b.where, " AND "))
	}
	if len(b.groupBy) > 0 {
		sb.WriteString(" GROUP BY ")
		sb.WriteString(b.buildGroupBy())
	}
	if b.orderByUnsafe != "" {
		sb.WriteString(" ORDER BY ")
		sb.WriteString(b.orderByUnsafe)
	} else if len(b.orderBy) > 0 {
		sb.WriteString(" ORDER BY ")
		sb.WriteString(b.buildOrderBy())
	}
	if b.limit > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, b.limit)
	}
	if b.offset > 0 {
		sb.WriteString(" OFFSET ?")
		args = append(args, b.offset)
	}
	if b.locking != "" {
		sb.WriteString(b.locking)
	}
	return sb.String(), args, nil
}

// buildOrderBy 把结构化排序规则转换成 ORDER BY 片段。
func (b *selectBuilder) buildOrderBy() string {
	parts := make([]string, 0, len(b.orderBy))
	for _, o := range b.orderBy {
		if o.Column == "" {
			continue
		}
		col := b.dialect.QuoteIdentifier(o.Column)
		if o.Desc {
			parts = append(parts, col+" DESC")
		} else {
			parts = append(parts, col+" ASC")
		}
	}
	return strings.Join(parts, ", ")
}

// Query 执行构建好的查询并返回结果集游标。
func (b *selectBuilder) Query(ctx context.Context) (db.IRows, error) {
	q, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return b.db.Query(ctx, q, args...)
}

// QueryRow 执行构建好的查询并返回单行结果游标。
func (b *selectBuilder) QueryRow(ctx context.Context) db.IRow {
	q, args, err := b.Build()
	if err != nil {
		return &errorRow{err: err}
	}
	return b.db.QueryRow(ctx, q, args...)
}

// buildTableName 生成 FROM 子句中的表表达式，并在安全标识符场景下按方言加引号。
func (b *selectBuilder) buildTableName() string {
	if b.tableUnsafe {
		return b.table
	}
	// From() 已保证 b.table 是安全标识符，这里只负责按方言 quote。
	return b.dialect.QuoteIdentifier(b.table)
}

// buildSelectColumns 生成 SELECT 列表，并只对安全标识符做方言转义。
func (b *selectBuilder) buildSelectColumns() string {
	cols := make([]string, len(b.cols))
	for i, col := range b.cols {
		if col == "*" {
			cols[i] = col
			continue
		}
		if b.colsUnsafe {
			cols[i] = col
		} else if safeident.IsSafeIdentifier(col) {
			cols[i] = b.dialect.QuoteIdentifier(col)
		} else {
			b.setErr(errors.NewCode(errors.InvalidInput, "selectBuilder: unsafe select column").
				WithContext("column", col))
			return ""
		}
	}
	return strings.Join(cols, ", ")
}

// buildGroupBy 生成 GROUP BY 列表，并按方言转义列名。
func (b *selectBuilder) buildGroupBy() string {
	group := make([]string, len(b.groupBy))
	for i, col := range b.groupBy {
		group[i] = b.dialect.QuoteIdentifier(col)
	}
	return strings.Join(group, ", ")
}

type errorRow struct {
	err error
}

// Scan 把当前结果写入目标对象。
func (r *errorRow) Scan(dest ...any) error {
	_ = dest
	return r.err
}

func (r *errorRow) Err() error { return r.err }
