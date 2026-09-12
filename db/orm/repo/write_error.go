package repo

import (
	"gochen/db"
	"gochen/errors"
)

// writeError 在仓储边界统一写入错误，保留驱动原因供诊断和 errors.As 使用。
func writeError(err error, message string) error {
	if errors.Is(err, errors.Conflict) || db.IsUniqueViolation(err) {
		return errors.Wrap(err, errors.Conflict, message)
	}
	return errors.Wrap(err, errors.Database, message)
}
