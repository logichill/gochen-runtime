package stdsql

import (
	"context"
	"database/sql"
	coredialect "gochen/db/dialect"
	"strings"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/safeident"
	"gochen/db"
	"gochen/errors"
)

// Tx 是 `database/sql` 事务在框架数据库抽象下的适配实现。
type Tx struct {
	db        *sql.DB
	tx        *sql.Tx
	dialect   coredialect.IDialect
	bindLimit int
}

// Query 在事务上下文内执行查询，并按当前方言重绑定占位符。
func (t *Tx) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if t == nil || t.tx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "tx is nil")
	}
	q := t.dialect.Rebind(query)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return &Rows{rows: rows}, nil
}

// QueryRow 在事务上下文内执行单行查询。
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if ctx == nil {
		return &Row{err: errors.NewCode(errors.InvalidInput, "ctx is nil")}
	}
	if t == nil || t.tx == nil {
		return &Row{err: errors.NewCode(errors.InvalidInput, "tx is nil")}
	}
	q := t.dialect.Rebind(query)
	return &Row{row: t.tx.QueryRowContext(ctx, q, args...)}
}

// Exec 在事务上下文内执行非查询 SQL。
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if t == nil || t.tx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "tx is nil")
	}
	q := t.dialect.Rebind(query)
	return t.tx.ExecContext(ctx, q, args...)
}

// Begin 明确拒绝嵌套事务，调用方如需细粒度回滚应使用 savepoint。
func (t *Tx) Begin(ctx context.Context) (db.ITransaction, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	return nil, errors.NewCode(errors.Unsupported, "nested transactions are not supported")
}

// BeginTx 明确拒绝在现有事务内部再开启嵌套事务。
func (t *Tx) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	_ = opts
	return nil, errors.NewCode(errors.Unsupported, "nested transactions are not supported")
}

// Ping 使用底层 `*sql.DB` 做一次连通性探测。
func (t *Tx) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if t == nil || t.db == nil {
		return errors.NewCode(errors.InvalidInput, "db is nil")
	}
	return t.db.PingContext(ctx)
}

// Close 以“未提交则回滚”的语义结束事务句柄。
func (t *Tx) Close() error {
	if t == nil || t.tx == nil {
		return nil
	}
	// 安全语义：Close 等价于“未提交则回滚”，以支持常见的 `defer tx.Close()` 用法。
	// 若事务已 Commit/Rollback，则 Rollback 会返回 sql.ErrTxDone，视为幂等成功。
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// SQLTx 返回底层 *sql.Tx，仅供 stdsql 适配层使用。
func (t *Tx) SQLTx() *sql.Tx { return t.tx }

// Commit 提交当前事务。
func (t *Tx) Commit() error {
	if t == nil || t.tx == nil {
		return errors.NewCode(errors.InvalidInput, "tx is nil")
	}
	return t.tx.Commit()
}

// CreateSavepoint 创建 savepoint。
func (t *Tx) CreateSavepoint(ctx context.Context, name string) error {
	if err := validateSavepointName(name); err != nil {
		return err
	}
	_, err := t.Exec(ctx, "SAVEPOINT "+name)
	return err
}

// RollbackToSavepoint 回滚到指定 savepoint。
func (t *Tx) RollbackToSavepoint(ctx context.Context, name string) error {
	if err := validateSavepointName(name); err != nil {
		return err
	}
	_, err := t.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
	return err
}

// ReleaseSavepoint 释放指定 savepoint。
func (t *Tx) ReleaseSavepoint(ctx context.Context, name string) error {
	if err := validateSavepointName(name); err != nil {
		return err
	}
	_, err := t.Exec(ctx, "RELEASE SAVEPOINT "+name)
	return err
}

// SupportsSavepoints 返回当前事务是否应暴露 savepoint 能力。
func (t *Tx) SupportsSavepoints() bool {
	if t == nil {
		return false
	}
	return t.dialect.SupportsSavepoints()
}

// Rollback 回滚当前事务。
func (t *Tx) Rollback() error {
	if t == nil || t.tx == nil {
		return nil
	}
	return t.tx.Rollback()
}

// DialectName 返回事务使用的底层数据库方言名称。
func (t *Tx) DialectName() string {
	return string(t.dialect.Name())
}

// MaxBindParameters 把预算透传进事务：批量写全部发生在事务里，
// 这里断掉的话事务内的分片就会悄悄退回默认值。
func (t *Tx) MaxBindParameters() int {
	if t == nil {
		return dialect.DefaultMaxBindParameters
	}
	return dialect.ResolveMaxBindParameters(t.bindLimit)
}

func validateSavepointName(name string) error {
	if strings.Contains(name, ".") || !safeident.IsSafeIdentifier(name) {
		return errors.NewCode(errors.InvalidInput, "invalid savepoint name").
			WithContext("savepoint", name)
	}
	return nil
}
