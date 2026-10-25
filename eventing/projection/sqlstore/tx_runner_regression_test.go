package sqlstore

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

// runnerItem 投影事务 runner 回归测试用的最小实体（可写版本字段）。
type runnerItem struct {
	ID      int64  `json:"id"`
	Version uint64 `json:"version"`
	Name    string `json:"name"`
}

func (i *runnerItem) GetID() int64         { return i.ID }
func (i *runnerItem) GetVersion() uint64   { return i.Version }
func (i *runnerItem) SetID(id int64)       { i.ID = id }
func (i *runnerItem) SetVersion(v uint64)  { i.Version = v }

func newRunnerFixture(tb testing.TB, name string) (db.IDatabase, *repo.Repo[*runnerItem, int64], *SQLCheckpointTxRunner) {
	tb.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(context.Background(),
		"CREATE TABLE items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0, name TEXT)")
	require.NoError(tb, err)
	engine, err := lite.New(database)
	require.NoError(tb, err)
	repository, err := repo.NewRepo[*runnerItem, int64](engine, "items")
	require.NoError(tb, err)
	runner, err := NewSQLCheckpointTxRunner(engine)
	require.NoError(tb, err)
	return database, repository, runner
}

func countRunnerRows(tb testing.TB, database db.IDatabase, id int64) int {
	tb.Helper()
	var n int
	row := database.QueryRow(context.Background(), "SELECT COUNT(*) FROM items WHERE id = ?", id)
	require.NoError(tb, row.Scan(&n))
	return n
}

// TestRegressionRunnerTx_InnerRepoFailureBlocksCommit 验证：投影事务作为外层
// 时同样受统一 rollback-only 语义约束——内层仓储事务失败被捕获后，物理提交点
// 必须拒绝，失败写入不得落库。
func TestRegressionRunnerTx_InnerRepoFailureBlocksCommit(t *testing.T) {
	database, repository, runner := newRunnerFixture(t, "reg-runner-inner-fail")

	innerErr := errors.NewCode(errors.InvalidInput, "abort inner")
	err := runner.WithinTx(context.Background(), func(txCtx context.Context) error {
		err := repository.WithinTx(txCtx, func(inner context.Context) error {
			if err := repository.Create(inner, &runnerItem{ID: 601, Name: "failed-inner"}); err != nil {
				return err
			}
			return innerErr
		})
		require.Error(t, err)
		return nil // 外层吞掉内层错误尝试提交
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))
	require.Equal(t, 0, countRunnerRows(t, database, 601))
}

// TestRegressionRunnerTx_SuccessPathCommits 验证正常路径不受影响：
// 投影事务内的仓储写入照常提交。
func TestRegressionRunnerTx_SuccessPathCommits(t *testing.T) {
	database, repository, runner := newRunnerFixture(t, "reg-runner-ok")

	err := runner.WithinTx(context.Background(), func(txCtx context.Context) error {
		return repository.Create(txCtx, &runnerItem{ID: 602, Name: "ok"})
	})
	require.NoError(t, err)
	require.Equal(t, 1, countRunnerRows(t, database, 602))
}
