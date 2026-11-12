package sqlstore

import (
	"context"
	"database/sql"
	"reflect"

	"gochen/contextx"
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

	session, _, joined := orm.TxSessionFromContext(ctx)
	if !joined {
		session, joined = r.ormEngine.(orm.IOrmSession)
	}
	if joined {
		sessionID, sessionOK := orm.DatasourceIDOf(session)
		engineID, engineOK := orm.DatasourceIDOf(r.ormEngine)
		sessionValue, engineValue := reflect.ValueOf(session), reflect.ValueOf(r.ormEngine)
		same := sessionValue.Type() == engineValue.Type() && sessionValue.Kind() == reflect.Pointer &&
			sessionValue.Pointer() == engineValue.Pointer()
		if !same && (!sessionOK || !engineOK || sessionID != engineID) {
			return errors.NewCode(errors.FailedPrecondition, "projection transaction session datasource does not match the orm engine")
		}
		if _, ok := contextx.TxLifecycleFromContext(ctx); !ok {
			if _, ok := orm.AfterCommitDispatcherFromSession(session); !ok {
				return errors.NewCode(errors.Unsupported, "projection transaction session cannot propagate post-commit callbacks")
			}
		}
	} else {
		var err error
		session, err = r.ormEngine.BeginTx(ctx, (*sql.TxOptions)(nil))
		if err != nil {
			return errors.Wrap(err, errors.Database, "begin projection transaction failed")
		}
	}
	return tx.RunTxLifecycle(ctx, checkpointSessionRunner{SessionRunner: tx.SessionRunner{Session: session}, owned: !joined}, fn)
}

type checkpointSessionRunner struct {
	tx.SessionRunner
	owned bool
}

func (r checkpointSessionRunner) BeginTx(ctx context.Context) (tx.TxScope, error) {
	txCtx, err := orm.WithTxSession(ctx, r.Session, r.owned)
	if err != nil {
		return tx.TxScope{}, err
	}
	return tx.NewTxScope(txCtx, r.owned)
}
