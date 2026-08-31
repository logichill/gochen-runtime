package sqlstore

import (
	"testing"

	"gochen/testkit/require"

	"gochen/db"
	"gochen/observe/logging"
)

// newTestStore 执行对应操作。
//
// 参数：
// - database：参数值（具体语义见函数上下文）（类型：db.IDatabase）
// - tableName：名称
//
// 返回：
// - result：返回的实例（类型：*SQLEventStore[int64]）
func newTestStore(tb testing.TB, database db.IDatabase, tableName string) *SQLEventStore[int64] {
	tb.Helper()
	store, err := NewSQLEventStore(database, tableName, WithLogger(logging.NewNoopLogger()))
	require.NoError(tb, err)
	return store
}
