package sqlbuilder

import (
	"context"
	"database/sql"
	"strings"

	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

type deleteBuilder struct {
	db      db.IDatabase
	dialect dialect.IDialect

	table string
	where []string
	args  []any
	limit int

	err error
}

// setErr 设置Err。
func (b *deleteBuilder) setErr(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

// Where 追加 DELETE 的 WHERE 条件。
//
// 参数：
// - cond：条件表达式（占位符使用 ?）
// - args：条件参数（用于填充占位符）
func (b *deleteBuilder) Where(cond string, args ...any) IDeleteBuilder {
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

// Limit 设置 DELETE 的最大影响行数（仅对支持的方言生效）。
//
// 参数：
// - n：最大影响行数（n<0 会 panic）
func (b *deleteBuilder) Limit(n int) IDeleteBuilder {
	if b.err != nil {
		return b
	}
	if n < 0 {
		b.setErr(errors.NewCode(errors.InvalidInput, "deleteBuilder: negative limit").WithContext("limit", n))
		return b
	}
	b.limit = n
	return b
}

// Build 构建数据。
//
// 说明：
// - Build 构建并返回结果。
func (b *deleteBuilder) Build() (string, []any, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	if !isSafeIdentifier(b.table) {
		return "", nil, errors.NewCode(errors.InvalidInput, "deleteBuilder: unsafe table name").WithContext("table", b.table)
	}

	var sb strings.Builder
	args := make([]any, len(b.args))
	copy(args, b.args)

	sb.WriteString("DELETE FROM ")
	sb.WriteString(b.dialect.QuoteIdentifier(b.table))

	if len(b.where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(b.where, " AND "))
	}

	if b.limit > 0 && b.dialect.SupportsDeleteLimit() {
		sb.WriteString(" LIMIT ?")
		args = append(args, b.limit)
	}

	return sb.String(), args, nil
}

// Exec 执行构建好的 DELETE 语句。
func (b *deleteBuilder) Exec(ctx context.Context) (sql.Result, error) {
	q, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return b.db.Exec(ctx, q, args...)
}
