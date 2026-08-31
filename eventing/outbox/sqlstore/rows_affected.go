package sqlstore

import (
	"database/sql"

	"gochen-runtime/internal/sqlresult"
	"gochen/errors"
)

func ensureRowsAffected(result sql.Result, expected int64, message string) errors.IAppError {
	return sqlresult.EnsureRowsAffected(result, expected, message)
}

func readRowsAffected(result sql.Result, message string) (int64, errors.IAppError) {
	return sqlresult.ReadRowsAffected(result, message, message)
}

func rowsAffectedError(err error, message string) errors.IAppError {
	return sqlresult.RowsAffectedError(err, message)
}
