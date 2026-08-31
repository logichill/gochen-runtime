package stdsql

import (
	"database/sql"

	"gochen/db"
)

// ISQLDBProvider 暴露底层 *sql.DB，仅供 stdsql 适配层使用。
type ISQLDBProvider interface {
	SQLDB() *sql.DB
}

// ISQLTxProvider 暴露底层 *sql.Tx，仅供 stdsql 适配层使用。
type ISQLTxProvider interface {
	SQLTx() *sql.Tx
}

// DBOf 从抽象数据库中提取底层 *sql.DB。
func DBOf(database db.IDatabase) (*sql.DB, bool) {
	provider, ok := database.(ISQLDBProvider)
	if !ok || provider == nil {
		return nil, false
	}
	raw := provider.SQLDB()
	return raw, raw != nil
}

// TxOf 从抽象事务中提取底层 *sql.Tx。
func TxOf(tx db.ITransaction) (*sql.Tx, bool) {
	provider, ok := tx.(ISQLTxProvider)
	if !ok || provider == nil {
		return nil, false
	}
	raw := provider.SQLTx()
	return raw, raw != nil
}
