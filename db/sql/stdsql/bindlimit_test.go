package stdsql

import (
	"context"
	"testing"

	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
	"gochen/testkit/require"

	_ "modernc.org/sqlite"
)

func newBindLimitDB(t *testing.T, maxBindParameters int) db.IDatabase {
	t.Helper()
	database, err := NewWithContext(context.Background(), db.DBConfig{
		Driver:            "sqlite",
		Database:          ":memory:",
		MaxOpenConns:      1,
		MaxIdleConns:      1,
		MaxBindParameters: maxBindParameters,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// 未配置时回落到默认预算，且必须实现能力接口——批量分片全靠这次断言发现它。
func TestDB_MaxBindParameters_DefaultsWhenUnset(t *testing.T) {
	database := newBindLimitDB(t, 0)

	provider, ok := database.(db.IBindParameterLimitProvider)
	require.True(t, ok, "DB 必须实现 IBindParameterLimitProvider，否则批量分片静默退回默认值")
	require.Equal(t, dialect.DefaultMaxBindParameters, provider.MaxBindParameters())
}

func TestDB_MaxBindParameters_HonorsConfiguredValue(t *testing.T) {
	database := newBindLimitDB(t, 999)

	provider, ok := database.(db.IBindParameterLimitProvider)
	require.True(t, ok)
	require.Equal(t, 999, provider.MaxBindParameters())
}

// 预算必须透传进事务：批量写全在事务里，断在这儿就等于没配。
func TestTx_MaxBindParameters_InheritsFromDB(t *testing.T) {
	database := newBindLimitDB(t, 999)

	tx, err := database.Begin(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	provider, ok := tx.(db.IBindParameterLimitProvider)
	require.True(t, ok, "事务同样要回答占位参数预算")
	require.Equal(t, 999, provider.MaxBindParameters())
}

// 过小的预算不会报错、只会把每条语句切碎，因此必须在构造期拒掉。
func TestNewWithContext_RejectsTooSmallBindParameterBudget(t *testing.T) {
	_, err := NewWithContext(context.Background(), db.DBConfig{
		Driver:            "sqlite",
		Database:          ":memory:",
		MaxBindParameters: dialect.MinMaxBindParameters - 1,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 负值同样是配置错误，不得被当成"用默认值"。
func TestNewWithContext_RejectsNegativeBindParameterBudget(t *testing.T) {
	_, err := NewWithContext(context.Background(), db.DBConfig{
		Driver:            "sqlite",
		Database:          ":memory:",
		MaxBindParameters: -1,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}
