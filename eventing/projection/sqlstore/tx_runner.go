package sqlstore

import (
	"context"
	"database/sql"

	"gochen/db/orm"
	"gochen/db/tx"
	"gochen/errors"
)

// SQLCheckpointTxRunner opens an ORM transaction and exposes the session through ctx.
type SQLCheckpointTxRunner struct {
	ormEngine orm.IOrm
}

// NewSQLCheckpointTxRunner 创建 SQL checkpoint 使用的 ORM 事务 runner。
func NewSQLCheckpointTxRunner(ormEngine orm.IOrm) (*SQLCheckpointTxRunner, error) {
	if ormEngine == nil {
		return nil, errors.NewCode(errors.InvalidInput, "projection orm cannot be nil")
	}
	return &SQLCheckpointTxRunner{ormEngine: ormEngine}, nil
}

// WithinTx 在 ORM 事务中执行 fn，并把事务 session 注入 ctx。
//
// 生命周期走统一的 tx.RunTxLifecycle：ctx 中建立共享 lifecycle，内层作用域
// （如 Repo.WithinTx / 应用服务事务）失败或 panic 时标记 rollback-only，
// 物理提交点统一检查并拒绝提交——本入口不再自持一套 begin/commit 旁路。
func (r *SQLCheckpointTxRunner) WithinTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if r == nil || r.ormEngine == nil {
		return errors.NewCode(errors.InvalidInput, "projection orm cannot be nil")
	}
	if fn == nil {
		return errors.NewCode(errors.InvalidInput, "fn is nil")
	}

	session, err := r.ormEngine.BeginTx(ctx, (*sql.TxOptions)(nil))
	if err != nil {
		return errors.Wrap(err, errors.Database, "begin projection transaction failed")
	}
	return tx.RunTxLifecycle(ctx, tx.SessionRunner{Session: session}, fn)
}
