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
	return s.tx.Rollback()
}

// AfterCommitDispatcher 暴露绑定在 session 上的提交后回调分发器。
func (s *session) AfterCommitDispatcher() contextx.IAfterCommitDispatcher {
	if s == nil {
		return nil
	}
	return s.afterCommit
}
