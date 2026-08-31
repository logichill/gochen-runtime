package sqlstore

import (
	"gochen-runtime/db/sql/safesql"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

func normalizeSQLTableName(database db.IDatabase, tableName, fallback string) (string, error) {
	quoted, err := safesql.QuoteQualifiedIdentifier(dialect.FromDatabase(database), tableName, fallback, "SQL table name", 2)
	if err != nil {
		return "", errors.NewCode(errors.InvalidInput, "invalid SQL table name").WithContext("cause", err.Error())
	}
	return quoted, nil
}
