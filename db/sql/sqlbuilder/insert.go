package sqlbuilder

import (
	"context"
	"database/sql"
	"strings"

	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

type insertBuilder struct {
	db      db.IDatabase
	dialect dialect.IDialect

	table   string
	columns []string
	rows    [][]any

	err error
}

// setErr 设置Err。
func (b *insertBuilder) setErr(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// Columns 设置 INSERT 的列名列表。
//
// 参数：
// - cols：列名列表（调用方需确保顺序与 Values 对齐）
func (b *insertBuilder) Columns(cols ...string) IInsertBuilder {
	b.columns = cols
	return b
}

// Values 追加一行待插入的数据。
//
// 参数：
// - vals：一行数据值（数量必须与 Columns 对齐）
func (b *insertBuilder) Values(vals ...any) IInsertBuilder {
	if b.err != nil {
		return b
	}
	if len(vals) == 0 {
		return b
	}
	b.rows = append(b.rows, vals)
	return b
}

// Build 构建数据。
//
// 说明：
// - Build 构建并返回结果。
func (b *insertBuilder) Build() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	if len(b.columns) == 0 {
		return "", nil, errors.NewCode(errors.InvalidInput, "insertBuilder: Columns is required")
	}
	if len(b.rows) == 0 {
		return "", nil, errors.NewCode(errors.InvalidInput, "insertBuilder: at least one row is required")
	}
	if !isSafeIdentifier(b.table) {
		return "", nil, errors.NewCode(errors.InvalidInput, "insertBuilder: unsafe table name").WithContext("table", b.table)
	}

	var sb strings.Builder
	args := make([]any, 0, len(b.rows)*len(b.columns))

	sb.WriteString("INSERT INTO ")
	sb.WriteString(b.dialect.QuoteIdentifier(b.table))
	sb.WriteString(" (")
	quotedCols := make([]string, len(b.columns))
	for i, col := range b.columns {
		if !isSafeIdentifier(col) {
			return "", nil, errors.NewCode(errors.InvalidInput, "insertBuilder: unsafe column name").WithContext("column", col)
		}
		quotedCols[i] = b.dialect.QuoteIdentifier(col)
	}
	sb.WriteString(strings.Join(quotedCols, ", "))
	sb.WriteString(") VALUES ")

	rowPlaceholder := "(" + strings.TrimRight(strings.Repeat("?, ", len(b.columns)), ", ") + ")"

	for i, row := range b.rows {
		if len(row) != len(b.columns) {
			return "", nil, errors.NewCode(errors.InvalidInput, "insertBuilder: values length mismatch columns length").
				WithContext("row_len", len(row)).
				WithContext("columns_len", len(b.columns))
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(rowPlaceholder)
		args = append(args, row...)
	}

	return sb.String(), args, nil
}

// Exec 执行构建好的 INSERT 语句。
func (b *insertBuilder) Exec(ctx context.Context) (sql.Result, error) {
	q, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return b.db.Exec(ctx, q, args...)
}
