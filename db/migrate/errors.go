package migrate

import (
	stderrors "errors"

	goerrors "gochen/errors"
)

var (
	// ErrNoChange 表示当前没有可执行的 migration 变更。
	ErrNoChange = stderrors.New("migrate: no change")
	// ErrDirty 表示数据库 migration 状态处于 dirty。
	ErrDirty = stderrors.New("migrate: dirty database version")
	// ErrNilVersion 表示当前没有已应用版本。
	ErrNilVersion = stderrors.New("migrate: no migration version")
	// ErrLocked 表示 migration lock 当前已被其他 runner 持有。
	ErrLocked = stderrors.New("migrate: locked")
	// ErrReviewRequired 表示 migration 文件包含人工 review 保护语句，runner 拒绝执行。
	ErrReviewRequired = stderrors.New("migrate: manual review required")
)

func dirtyStateError(version uint64) error {
	return goerrors.NewCodeWithCause(goerrors.FailedPrecondition, "migration state is dirty", ErrDirty).
		WithContext("version", version)
}
