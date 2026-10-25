package repo

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"gochen/contextx"
	"gochen/db/orm"
	"gochen/db/tx"
	"gochen/errors"
)

func ensureAfterCommitPropagation(ctx context.Context, session orm.IOrmSession) error {
	if _, ok := contextx.TxLifecycleFromContext(ctx); ok {
		return nil
	}
	if _, ok := orm.AfterCommitDispatcherFromSession(session); ok {
		return nil
	}
	return errors.NewCode(errors.Unsupported,
		"transaction session cannot propagate post-commit callbacks; use an after-commit-aware session or wrap the outer commit with app/crud.WithTx",
	)
}

func (r *Repo[T, ID]) beginTx(ctx context.Context) (tx.TxScope, error) {
	if ctx == nil {
		return tx.TxScope{}, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	// 已在事务中：复用外层事务（当前作用域不应再次提交/回滚）。
	// 复用前必须校验会话归属，防止把本仓储的写入路由进别的数据源的事务。
	if session, _, ok := orm.TxSessionFromContext(ctx); ok {
		if err := r.ensureSessionDatasource(session); err != nil {
			return tx.TxScope{}, err
		}
		if err := ensureAfterCommitPropagation(ctx, session); err != nil {
			return tx.TxScope{}, err
		}
		txCtx, err := orm.WithTxSession(ctx, session, false)
		if err != nil {
			return tx.TxScope{}, err
		}
		return tx.NewTxScope(txCtx, false)
	}
	if session, ok := r.orm.(orm.IOrmSession); ok && session != nil {
		if err := ensureAfterCommitPropagation(ctx, session); err != nil {
			return tx.TxScope{}, err
		}
		txCtx, err := orm.WithTxSession(ctx, session, false)
		if err != nil {
			return tx.TxScope{}, err
		}
		return tx.NewTxScope(txCtx, false)
	}

	session, err := r.orm.BeginTx(ctx, (*sql.TxOptions)(nil))
	if err != nil {
		return tx.TxScope{}, err
	}
	txCtx, err := orm.WithTxSession(ctx, session, true)
	if err != nil {
		return tx.TxScope{}, err
	}
	return tx.NewTxScope(txCtx, true)
}

// BeginTx 开启仓储事务并返回带 lifecycle 元数据的事务作用域。
func (r *Repo[T, ID]) BeginTx(ctx context.Context) (tx.TxScope, error) {
	return r.beginTx(ctx)
}

// Commit 提交仓储事务作用域。
func (r *Repo[T, ID]) Commit(tx tx.TxScope) error {
	return r.commitTx(tx)
}

// Rollback 回滚仓储事务作用域。
func (r *Repo[T, ID]) Rollback(tx tx.TxScope) error {
	return r.rollbackTx(tx)
}

// WithinTx 在同一事务中执行 fn，并在成功时提交、失败时回滚。
//
// 嵌套语义：内层 WithinTx 复用外层事务。内层 fn 失败会默认把整个事务标记为
// rollback-only——即使外层捕获该错误继续返回 nil，提交也会被拒绝并回滚，
// 失败写入不会静默落库。需要“内层失败只撤销内层、外层继续”的语义时，
// 显式使用 WithinSavepoint。
func (r *Repo[T, ID]) WithinTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	return tx.RunTxLifecycle(ctx, r, fn)
}

// savepointSeq 生成进程内唯一的保存点名。
var savepointSeq atomic.Uint64

// WithinSavepoint 在当前事务内以保存点执行 fn：fn 失败（返回 error 或 panic）时
// 只回滚 fn 内的写入与 fn 内注册的提交后回调，事务其余部分不受影响，调用方可
// 吞掉错误继续外层流程。
//
// 失败边界：保存点作用域具有独立的失败边界——fn 内部经 WithinTx 等嵌套作用域
// 产生的 rollback-only 标记，只有在该失败对应的写入被**物理回滚**后才作废
// （进入保存点前已有的失败状态始终保持）。两条防线保证这一点：
//   - fn 返回 nil 但内部留有未处理的失败标记（调用方吞掉了嵌套错误）：
//     失败写入尚未撤销、其回调已被清除，不能当作成功——保存点整体回滚并
//     返回错误，调用方得到确定性的失败信号；
//   - 释放保存点失败：先尝试物理回滚本层写入与回调；撤销无法确认成功时
//     保持整体 rollback-only，禁止外层提交残留写入。
//
// 约束：
//   - 必须处于活动事务中（通常在 WithinTx 的 fn 内调用），否则返回 FailedPrecondition；
//   - 会话必须支持保存点（见 orm.ISavepointSession），否则返回 Unsupported。
func (r *Repo[T, ID]) WithinSavepoint(ctx context.Context, fn func(spxCtx context.Context) error) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if fn == nil {
		return errors.NewCode(errors.InvalidInput, "fn is nil")
	}
	session, _, ok := orm.TxSessionFromContext(ctx)
	if !ok || session == nil {
		return errors.NewCode(errors.FailedPrecondition,
			"savepoint requires an active transaction; call WithinSavepoint inside WithinTx")
	}
	if err := r.ensureSessionDatasource(session); err != nil {
		return err
	}
	sp, ok := session.(orm.ISavepointSession)
	if !ok || sp == nil {
		return errors.NewCode(errors.Unsupported,
			"transaction session does not support savepoints; keep the default nested rollback-only semantics instead")
	}
	name := fmt.Sprintf("gochen_sp_%d", savepointSeq.Add(1))
	level := contextx.MarkAfterCommitLevel(ctx)
	enteredRollbackOnly := contextx.TxRollbackOnly(ctx)
	if err := sp.CreateSavepoint(ctx, name); err != nil {
		return err
	}
	defer func() {
		if rec := recover(); rec != nil {
			// panic 与返回 error 同待遇：撤销内层写入与回调后原样重抛，
			// 不给"外层 recover 后静默提交内层写入"留路径。
			r.rollbackSavepointScope(ctx, sp, name, level, enteredRollbackOnly)
			panic(rec)
		}
	}()
	if err := fn(ctx); err != nil {
		r.rollbackSavepointScope(ctx, sp, name, level, enteredRollbackOnly)
		return err
	}
	// fn 返回 nil 但内部留有未处理的失败标记：某个嵌套作用域失败被 fn 吞掉，
	// 其写入仍在事务里、其回调已被清除——数据与后续动作不一致，绝不能当成功。
	// 与显式失败同待遇：物理回滚保存点、截断回调并返回错误。
	if !enteredRollbackOnly && contextx.TxRollbackOnly(ctx) {
		r.rollbackSavepointScope(ctx, sp, name, level, enteredRollbackOnly)
		return errors.NewCode(errors.FailedPrecondition,
			"savepoint scope swallowed an inner failure; rolled back to savepoint")
	}
	if err := sp.ReleaseSavepoint(ctx, name); err != nil {
		// 释放失败：本层写入的去留已不确定。先尝试物理回滚与回调截断；
		// 回滚也无法确认成功时保持整体 rollback-only，外层提交必须被拒绝。
		contextx.TruncateAfterCommitTo(ctx, level)
		if rbErr := sp.RollbackToSavepoint(ctx, name); rbErr != nil {
			contextx.MarkTxRollbackOnly(ctx)
			return errors.Join(
				errors.Wrap(err, errors.Database, "failed to release savepoint"),
				errors.Wrap(rbErr, errors.Database, "failed to roll back to savepoint after release failure"),
			)
		}
		if !enteredRollbackOnly {
			contextx.ClearTxRollbackOnly(ctx)
		}
		return errors.Wrap(err, errors.Database, "failed to release savepoint; rolled back to savepoint")
	}
	// 到达这里时本作用域内不可能残留失败标记（有标记的分支已在上方回滚返回），
	// 清除只是防御性兜底；进入前已有的失败状态保持不变。
	if !enteredRollbackOnly {
		contextx.ClearTxRollbackOnly(ctx)
	}
	return nil
}

// rollbackSavepointScope 撤销保存点作用域的写入与回调。
// 回滚保存点失败时把整体事务标记为 rollback-only：局部撤销已不可信，
// 外层提交必须在提交点被拒绝。回滚成功时，本作用域内产生的 rollback-only
// 标记随失败一起作废（进入前已有的保持不变）。
func (r *Repo[T, ID]) rollbackSavepointScope(ctx context.Context, sp orm.ISavepointSession, name string, level contextx.AfterCommitLevel, enteredRollbackOnly bool) {
	contextx.TruncateAfterCommitTo(ctx, level)
	if err := sp.RollbackToSavepoint(ctx, name); err != nil {
		contextx.MarkTxRollbackOnly(ctx)
		return
	}
	if !enteredRollbackOnly {
		contextx.ClearTxRollbackOnly(ctx)
	}
}

func (r *Repo[T, ID]) commitTx(scope tx.TxScope) error {
	session, owned, ok := orm.TxSessionFromContext(scope.Context())
	if !ok {
		return errors.NewCode(errors.InvalidInput, "transaction not started")
	}
	if !owned || !scope.Owned() {
		return nil
	}
	// 提交点统一走 tx.CommitOwnedSession：rollback-only 拒绝、提交、封存回调
	// 与所有事务入口（投影 runner、SessionRunner 等）保持同一实现。
	return tx.CommitOwnedSession(scope.Context(), session)
}

func (r *Repo[T, ID]) rollbackTx(scope tx.TxScope) error {
	session, owned, ok := orm.TxSessionFromContext(scope.Context())
	if !ok {
		return errors.NewCode(errors.InvalidInput, "transaction not started")
	}
	if !owned || !scope.Owned() {
		return nil
	}
	return tx.RollbackOwnedSession(scope.Context(), session)
}
