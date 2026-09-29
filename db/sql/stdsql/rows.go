package stdsql

import "database/sql"

// Rows 定义Rows。
type Rows struct{ rows *sql.Rows }

// Next 推进到下一项并返回是否成功。
func (r *Rows) Next() bool { return r.rows.Next() }

// Scan 把当前结果写入目标对象。
func (r *Rows) Scan(dest ...any) error { return r.rows.Scan(dest...) }

// Close 关闭并释放资源。
func (r *Rows) Close() error { return r.rows.Close() }

// Err 返回结果集迭代期间遇到的错误。
func (r *Rows) Err() error { return r.rows.Err() }

// Columns 返回查询结果的列名。
func (r *Rows) Columns() ([]string, error) { return r.rows.Columns() }

// ColumnTypes 返回查询结果的列类型描述。
func (r *Rows) ColumnTypes() ([]*sql.ColumnType, error) { return r.rows.ColumnTypes() }

// Row 定义行。
type Row struct {
	row *sql.Row
	err error
}

// Scan 把当前结果写入目标对象。
func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.row == nil {
		return sql.ErrNoRows
	}
	return r.row.Scan(dest...)
}

// Err 返回延迟查询错误；若构造阶段已有错误则优先返回该错误。
func (r *Row) Err() error {
	if r.err != nil {
		return r.err
	}
	if r.row == nil {
		return nil
	}
	return r.row.Err()
}
