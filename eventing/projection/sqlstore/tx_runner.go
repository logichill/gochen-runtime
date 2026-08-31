package sqlstore

import (
	"context"
	"database/sql"

	"gochen/db/orm"
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
	txCtx, err := orm.WithTxSession(ctx, session, true)
	if err != nil {
		_ = session.Rollback()
		return err
	}

	committed := false
	defer func() {
		if !committed {
			_ = session.Rollback()
		}
	}()

	if err := fn(txCtx); err != nil {
		return err
	}
	if err := session.Commit(); err != nil {
		return errors.Wrap(err, errors.Database, "commit projection transaction failed")
	}
	committed = true
	return nil
}
