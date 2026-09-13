package sqlbuilder

import (
	"context"
	"database/sql"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen-runtime/internal/sqlresult"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

// upsertBuilder 负责构造方言无关的“先插入、冲突则更新”语句。
type upsertBuilder struct {
	db      db.IDatabase
	dialect dialect.IDialect

	table        string
	columns      []string
	values       []any
	keyColumns   []string
	updateValues map[string]any
}

// Columns 设置 UPSERT 的插入列名列表。
//
// 参数：
// - cols：列名列表（调用方需确保顺序与 Values 对齐）
func (b *upsertBuilder) Columns(cols ...string) IUpsertBuilder {
	b.columns = cols
	return b
}

// Values 设置待插入的一行数据。
//
// 参数：
// - vals：一行数据值（数量必须与 Columns 对齐）
func (b *upsertBuilder) Values(vals ...any) IUpsertBuilder {
	b.values = vals
	return b
}

// Key 设置冲突键列（用于匹配 ON CONFLICT/ON DUPLICATE KEY）。
//
// 参数：
// - cols：冲突键列名列表。
func (b *upsertBuilder) Key(cols ...string) IUpsertBuilder {
	b.keyColumns = cols
	return b
}

// UpdateSet 设置冲突更新时要写入的列值。
//
// 参数：
// - col：列名。
func (b *upsertBuilder) UpdateSet(col string, val any) IUpsertBuilder {
	if b.updateValues == nil {
		b.updateValues = make(map[string]any)
	}
	b.updateValues[col] = val
	return b
}

// UpdateSetMap 批量设置冲突更新时要写入的列值。
func (b *upsertBuilder) UpdateSetMap(values map[string]any) IUpsertBuilder {
	if b.updateValues == nil {
		b.updateValues = make(map[string]any)
	}
	for k, v := range values {
		b.updateValues[k] = v
	}
	return b
}

// Exec 执行构建好的 UPSERT 语句。
func (b *upsertBuilder) Exec(ctx context.Context) (sql.Result, error) {
	if len(b.columns) == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "upsert: Columns is required")
	}
	if len(b.values) != len(b.columns) {
		return nil, errors.NewCode(errors.InvalidInput, "upsert: values length mismatch columns length")
	}
	if len(b.keyColumns) == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "upsert: Key is required")
	}

	ins := &insertBuilder{
		db:      b.db,
		dialect: b.dialect,
		table:   b.table,
		columns: b.columns,
		rows:    [][]any{b.values},
	}

	insertSQL, insertArgs, err := ins.Build()
	if err != nil {
		return nil, err
	}

	whereParts := make([]string, 0, len(b.keyColumns))
	whereArgs := make([]any, 0, len(b.keyColumns))

	colIndex := func(col string) int {
		for i, c := range b.columns {
			if c == col {
				return i
			}
		}
		return -1
	}

	for _, key := range b.keyColumns {
		if !safeident.IsSafeIdentifier(key) {
			return nil, errors.NewCode(errors.InvalidInput, "upsert: unsafe key column").
				WithContext("key_column", key)
		}
		idx := colIndex(key)
		if idx < 0 {
			return nil, errors.NewCode(errors.InvalidInput, "upsert: key column not found in Columns").
				WithContext("key_column", key)
		}
		whereParts = append(whereParts, b.dialect.QuoteIdentifier(key)+" = ?")
		whereArgs = append(whereArgs, b.values[idx])
	}

	updateVals := make(map[string]any)
	if len(b.updateValues) == 0 {
		for i, col := range b.columns {
			skip := false
			for _, key := range b.keyColumns {
				if key == col {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			updateVals[col] = b.values[i]
		}
	} else {
		updateVals = b.updateValues
	}

	upd := &updateBuilder{
		db:      b.db,
		dialect: b.dialect,
		table:   b.table,
	}
	upd.SetMap(updateVals)
	upd.Where(strings.Join(whereParts, " AND "), whereArgs...)

	updateSQL, updateArgs, err := upd.Build()
	if err != nil {
		return nil, err
	}

	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		res, err := b.db.Exec(ctx, insertSQL, insertArgs...)
		if err == nil {
			return res, nil
		}

		if !b.dialect.IsUniqueViolation(err) {
			return nil, err
		}

		// 发生唯一键冲突时，尝试 UPDATE；若没有任何行被更新，则认为记录在期间被删除，重试 INSERT。
		res, err = b.db.Exec(ctx, updateSQL, updateArgs...)
		if err != nil {
			return nil, err
		}
		rows, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return nil, sqlresult.RowsAffectedError(rowsErr, "upsert: rows affected failed")
		}
		if rows > 0 {
			return res, nil
		}
	}

	return nil, errors.NewCode(errors.Concurrency, "upsert: failed after max attempts due to concurrent modifications").
		WithContext("max_attempts", maxAttempts)
}
