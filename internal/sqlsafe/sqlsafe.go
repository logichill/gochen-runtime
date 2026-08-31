package sqlsafe

import (
	"gochen-runtime/db/sql/safeident"
	"gochen/errors"
)

// DefaultInsertMaxParams 是事件存储和 Outbox 批量写入共同采用的保守参数上限。
const DefaultInsertMaxParams = 900

// ValidateTableName 校验 SQL 表名是否安全，避免拼接 SQL 时引入注入风险。
func ValidateTableName(tableName string) error {
	if err := safeident.ValidateQualifiedIdentifier(tableName, "table name", 2); err != nil {
		return errors.NewCode(errors.InvalidInput, "invalid table name").WithContext("cause", err.Error())
	}
	return nil
}
