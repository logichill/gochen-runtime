package sqlbuilder

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

type updateBuilder struct {
	db      db.IDatabase
	dialect dialect.IDialect

	table     string
	setCols   []string
	setArgs   []any
	exprSet   []string
	exprArgs  []any
	whereExpr []string
	whereArgs []any

	err error
}

// setErr 设置Err。
func (b *updateBuilder) setErr(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

func (b *updateBuilder) Set(col string, val any) IUpdateBuilder {
	if b.err != nil {
		return b
	}
	if col == "" {
		return b
	}
	b.setCols = append(b.setCols, col)
	b.setArgs = append(b.setArgs, val)
	return b
}

// SetMap 批量追加 SET 列赋值（按列名排序以获得稳定 SQL）。
func (b *updateBuilder) SetMap(values map[string]any) IUpdateBuilder {
	if len(values) == 0 {
		return b
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.Set(k, values[k])
	}
	return b
}

// SetIncrement 设置Increment。
func (b *updateBuilder) SetIncrement(column string, delta any) IUpdateBuilder {
	if b.err != nil {
		return b
	}
	if column == "" {
		return b
	}
	if !safeident.IsSafeIdentifier(column) {
		b.setErr(errors.NewCode(errors.InvalidInput, "updateBuilder: unsafe column name").WithContext("column", column))
		return b
	}

	col := b.dialect.QuoteIdentifier(column)
	b.exprSet = append(b.exprSet, col+" = "+col+" + ?")
	b.exprArgs = append(b.exprArgs, delta)
	return b
}

// SetCaseWhenEq 设置CaseWhenEq。
func (b *updateBuilder) SetCaseWhenEq(column string, whenColumn string, cases []CaseWhenEq) IUpdateBuilder {
	if b.err != nil {
		return b
	}
	if column == "" || whenColumn == "" {
		return b
	}
	if len(cases) == 0 {
		b.setErr(errors.NewCode(errors.InvalidInput, "updateBuilder: cases cannot be empty"))
		return b
	}
	if !safeident.IsSafeIdentifier(column) {
		b.setErr(errors.NewCode(errors.InvalidInput, "updateBuilder: unsafe column name").WithContext("column", column))
		return b
	}
	if !safeident.IsSafeIdentifier(whenColumn) {
		b.setErr(errors.NewCode(errors.InvalidInput, "updateBuilder: unsafe when column name").WithContext("column", whenColumn))
		return b
	}

	setCol := b.dialect.QuoteIdentifier(column)
	condCol := b.dialect.QuoteIdentifier(whenColumn)

	var sb strings.Builder
	sb.WriteString(setCol)
	sb.WriteString(" = CASE")
	for _, c := range cases {
		sb.WriteString(" WHEN ")
		sb.WriteString(condCol)
		sb.WriteString(" = ? THEN ?")
		b.exprArgs = append(b.exprArgs, c.When, c.Then)
	}
	sb.WriteString(" END")
	b.exprSet = append(b.exprSet, sb.String())
	return b
}

// SetExprUnsafe 直接追加原始 SET 片段（调用方需确保安全）。
func (b *updateBuilder) SetExprUnsafe(expr string, args ...any) IUpdateBuilder {
	if b.err != nil {
		return b
	}
	if expr == "" {
		return b
	}
	b.exprSet = append(b.exprSet, expr)
	if len(args) > 0 {
		b.exprArgs = append(b.exprArgs, args...)
	}
	return b
}

// Where 追加 UPDATE 的 WHERE 条件。
//
// 参数：
// - cond：条件表达式（占位符使用 ?）
// - args：条件参数（用于填充占位符）
func (b *updateBuilder) Where(cond string, args ...any) IUpdateBuilder {
	if b.err != nil {
		return b
	}
	if cond != "" {
		expanded, flat, err := expandPlaceholders(cond, args, b.dialect)
		if err != nil {
			b.setErr(err)
			return b
		}
		b.whereExpr = append(b.whereExpr, expanded)
		b.whereArgs = append(b.whereArgs, flat...)
	}
	return b
}

// Build 构建数据。
//
// 说明：
// - Build 构建并返回结果。
func (b *updateBuilder) Build() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	if len(b.setCols) == 0 && len(b.exprSet) == 0 {
		return "", nil, errors.NewCode(errors.InvalidInput, "updateBuilder: no columns or expressions to set")
	}
	if !safeident.IsSafeIdentifier(b.table) {
		return "", nil, errors.NewCode(errors.InvalidInput, "updateBuilder: unsafe table name").WithContext("table", b.table)
	}

	var sb strings.Builder
	args := make([]any, 0, len(b.setArgs)+len(b.exprArgs)+len(b.whereArgs))

	sb.WriteString("UPDATE ")
	sb.WriteString(b.dialect.QuoteIdentifier(b.table))
	sb.WriteString(" SET ")

	first := true
	for i, col := range b.setCols {
		if !first {
			sb.WriteString(", ")
		}
		first = false
		if !safeident.IsSafeIdentifier(col) {
			return "", nil, errors.NewCode(errors.InvalidInput, "updateBuilder: unsafe column name").WithContext("column", col)
		}
		sb.WriteString(b.dialect.QuoteIdentifier(col))
		sb.WriteString(" = ?")
		args = append(args, b.setArgs[i])
	}
	for _, expr := range b.exprSet {
		if !first {
			sb.WriteString(", ")
		}
		first = false
		sb.WriteString(expr)
		// exprArgs 在下面统一追加
	}

	args = append(args, b.exprArgs...)

	if len(b.whereExpr) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(b.whereExpr, " AND "))
		args = append(args, b.whereArgs...)
	}

	return sb.String(), args, nil
}

func (b *updateBuilder) Exec(ctx context.Context) (sql.Result, error) {
	q, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return b.db.Exec(ctx, q, args...)
}
