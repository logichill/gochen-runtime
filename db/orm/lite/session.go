package lite

import (
	"gochen/contextx"
	"gochen/db"
	"gochen/errors"
)

// session 实现 IOrmSession，委托给内部 Orm，并持有事务以便 Commit/Rollback。
type session struct {
	*Orm
	tx          db.ITransaction
	afterCommit contextx.IAfterCommitDispatcher
}

// Commit 提交事务。
func (s *session) Commit() error {
	if s.tx == nil {
		return errors.NewCode(errors.InvalidInput, "basic.session: tx is nil")
	}
	if err := s.tx.Commit(); err != nil {
		return err
	}
	if s.afterCommit == nil {
		return nil
	}
	if err := s.afterCommit.RunAfterCommit(); err != nil {
		return contextx.WrapAfterCommitError(err)
	}
	return nil
}

// Rollback 回滚事务。
func (s *session) Rollback() error {
	if s.tx == nil {
		return errors.NewCode(errors.InvalidInput, "basic.session: tx is nil")
	}
	err := s.tx.Rollback()
	// 回滚后提交后回调不会再判发：清空残留条目（及其捕获的 context 引用）
	// 并复位 drained，保证会话/分发器可复用、不泄漏内存。
	if dispatcher, ok := s.afterCommit.(interface{ Reset() }); ok {
		dispatcher.Reset()
	}
	return err
}

// AfterCommitDispatcher 暴露绑定在 session 上的提交后回调分发器。
func (s *session) AfterCommitDispatcher() contextx.IAfterCommitDispatcher {
	if s == nil {
		return nil
	}
	return s.afterCommit
}
