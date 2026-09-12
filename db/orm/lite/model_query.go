package lite

import (
	"context"
	"fmt"

	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/errors"
)

// First 查询单条记录。
func (m *model) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	qo := orm.CollectQueryOptions(opts...)

	d := dialect.FromDatabase(m.orm.db)
	builder, err := newSelectBuilder(m.orm.sql, d, qo)
	if err != nil {
		return err
	}
	tableExpr, err := buildTableExpr(d, m.table, qo.Joins)
	if err != nil {
		return err
	}
	if len(qo.Joins) == 0 {
		builder = builder.From(tableExpr)
	} else {
		builder = builder.FromUnsafe(tableExpr)
	}

	for _, w := range qo.Where {
		builder = builder.Where(w.Expr, w.Args...)
	}
	if len(qo.GroupBy) > 0 {
		builder = builder.GroupBy(qo.GroupBy...)
	}
	if len(qo.OrderBy) > 0 {
		orders, oerr := buildOrderBy(qo.OrderBy)
		if oerr != nil {
			return oerr
		}
		builder = builder.OrderBy(orders...)
	}
	// First 至少限制一条
	if qo.Limit > 0 {
		builder = builder.Limit(qo.Limit)
	} else {
		builder = builder.Limit(1)
	}
	if qo.Offset > 0 {
		builder = builder.Offset(qo.Offset)
	}
	if qo.ForUpdate {
		builder = builder.ForUpdate()
	}

	rows, err := builder.Query(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return errors.NewCode(errors.NotFound, "record not found")
	}

	if err := scanRowsIntoDest(rows, dest, m.orm); err != nil {
		return err
	}

	return nil
}

// Find 从存储中查询对象。
//
// 说明：
// - Find 查询多条记录。
func (m *model) Find(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	if err := validateFindDest(dest); err != nil {
		return err
	}

	qo := orm.CollectQueryOptions(opts...)

	d := dialect.FromDatabase(m.orm.db)
	builder, err := newSelectBuilder(m.orm.sql, d, qo)
	if err != nil {
		return err
	}
	tableExpr, err := buildTableExpr(d, m.table, qo.Joins)
	if err != nil {
		return err
	}
	if len(qo.Joins) == 0 {
		builder = builder.From(tableExpr)
	} else {
		builder = builder.FromUnsafe(tableExpr)
	}

	for _, w := range qo.Where {
		builder = builder.Where(w.Expr, w.Args...)
	}
	if len(qo.GroupBy) > 0 {
		builder = builder.GroupBy(qo.GroupBy...)
	}
	if len(qo.OrderBy) > 0 {
		orders, oerr := buildOrderBy(qo.OrderBy)
		if oerr != nil {
			return oerr
		}
		builder = builder.OrderBy(orders...)
	}
	if qo.Limit > 0 {
		builder = builder.Limit(qo.Limit)
	}
	if qo.Offset > 0 {
		builder = builder.Offset(qo.Offset)
	}
	if qo.ForUpdate {
		builder = builder.ForUpdate()
	}

	rows, err := builder.Query(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !isSliceDest(dest) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return errors.NewCode(errors.NotFound, "record not found")
		}
		return scanRowsIntoDest(rows, dest, m.orm)
	}
	return scanRowsIntoDest(rows, dest, m.orm)
}

// Count 统计数据。
func (m *model) Count(ctx context.Context, opts ...orm.QueryOption) (int64, error) {
	qo := orm.CollectQueryOptions(opts...)

	tableExpr, err := buildTableExpr(dialect.FromDatabase(m.orm.db), m.table, qo.Joins)
	if err != nil {
		return 0, err
	}

	if len(qo.GroupBy) > 0 {
		inner := m.orm.sql.SelectExprUnsafe("1")
		if len(qo.Joins) == 0 {
			inner = inner.From(tableExpr)
		} else {
			inner = inner.FromUnsafe(tableExpr)
		}
		for _, w := range qo.Where {
			inner = inner.Where(w.Expr, w.Args...)
		}
		inner = inner.GroupBy(qo.GroupBy...)
		innerSQL, innerArgs, err := inner.Build()
		if err != nil {
			return 0, err
		}
		row := m.orm.db.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM (%s) AS _gochen_count", innerSQL), innerArgs...)
		var count int64
		if err := row.Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}

	builder := m.orm.sql.SelectExprUnsafe("COUNT(*)")
	if len(qo.Joins) == 0 {
		builder = builder.From(tableExpr)
	} else {
		builder = builder.FromUnsafe(tableExpr)
	}
	for _, w := range qo.Where {
		builder = builder.Where(w.Expr, w.Args...)
	}

	row := builder.QueryRow(ctx)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
