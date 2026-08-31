package sqlresult

import (
	"database/sql/driver"

	"gochen/errors"
)

// IRowsAffectedResult 表示可返回受影响行数的 SQL 执行结果。
type IRowsAffectedResult interface {
	RowsAffected() (int64, error)
}

// ReadRowsAffected 读取 SQL 执行结果的受影响行数，并把驱动错误归一化为 AppError。
func ReadRowsAffected(result IRowsAffectedResult, nilMessage string, errMessage string) (int64, errors.IAppError) {
	if result == nil {
		return 0, errors.NewCode(errors.Internal, nilMessage)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, RowsAffectedError(err, errMessage)
	}
	return affected, nil
}

// EnsureRowsAffected 校验 SQL 执行结果命中期望行数。
func EnsureRowsAffected(result IRowsAffectedResult, expected int64, message string) errors.IAppError {
	rows, err := ReadRowsAffected(result, message, message)
	if err != nil {
		return err
	}
	if rows != expected {
		return errors.NewCode(errors.Conflict, message).
			WithContext("expected_rows", expected).
			WithContext("affected_rows", rows)
	}
	return nil
}

// RowsAffectedError 把 RowsAffected 返回的驱动错误转换为统一错误码。
func RowsAffectedError(err error, message string) errors.IAppError {
	if err == nil {
		return nil
	}
	if errors.Is(err, driver.ErrSkip) {
		return errors.Wrap(err, errors.Unsupported, message).
			WithContext("reason", "rows_affected_unsupported")
	}
	return errors.Wrap(err, errors.Database, message)
}
