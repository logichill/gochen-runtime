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

func (r *Rows) Err() error { return r.rows.Err() }

func (r *Rows) Columns() ([]string, error) { return r.rows.Columns() }

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

func (r *Row) Err() error { return r.err }
