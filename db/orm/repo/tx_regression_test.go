package repo

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

// crossItem 是 F01/F02 回归测试用的最小实体（可写版本字段，未启用审计）。
type crossItem struct {
	ID      int64  `json:"id"`
	Version uint64 `json:"version"`
	Name    string `json:"name"`
}

func (i *crossItem) GetID() int64       { return i.ID }
func (i *crossItem) GetVersion() uint64 { return i.Version }
func (i *crossItem) SetID(id int64)     { i.ID = id }
func (i *crossItem) SetVersion(v uint64) { i.Version = v }

// newCrossRepoDB 建立独立的内存数据库与仓储。
func newCrossRepoDB(tb testing.TB, name string) (db.IDatabase, *Repo[*crossItem, int64]) {
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
	repository, err := NewRepo[*crossItem, int64](engine, "items")
	require.NoError(tb, err)
	return database, repository
}

func countCrossRows(tb testing.TB, database db.IDatabase, id int64) int {
	tb.Helper()
	var n int
	row := database.QueryRow(context.Background(), "SELECT COUNT(*) FROM items WHERE id = ?", id)
	require.NoError(tb, row.Scan(&n))
	return n
}

// TestRegressionF01_CrossDatasourceSessionRejected 验证：A 事务中调用 B 仓储
// 必须在写入前报错，两侧数据库均不产生记录；同源嵌套不受影响。
func TestRegressionF01_CrossDatasourceSessionRejected(t *testing.T) {
	_, repoA := newCrossRepoDB(t, "reg-f01-a")
	dbB, repoB := newCrossRepoDB(t, "reg-f01-b")

	// 异源：B 仓储复用 A 事务必须被拒绝。
	err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		return repoB.Create(ctx, &crossItem{ID: 101, Name: "belongs-to-B"})
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))
	require.Equal(t, 0, countCrossRows(t, dbB, 101))

	// 同源：同一数据库的仓储在事务中协作不受影响。
	dbA2, repoA2 := newCrossRepoDB(t, "reg-f01-a2")
	err = repoA2.WithinTx(context.Background(), func(ctx context.Context) error {
		return repoA2.Create(ctx, &crossItem{ID: 102, Name: "same-source"})
	})
	require.NoError(t, err)
	require.Equal(t, 1, countCrossRows(t, dbA2, 102))
}

// TestRegressionF02_InnerFailureMarksRollbackOnly 验证：内层事务失败后，即使
// 外层捕获错误返回 nil，提交也必须被拒绝并回滚，失败写入不得落库。
func TestRegressionF02_InnerFailureMarksRollbackOnly(t *testing.T) {
	dbA, repoA := newCrossRepoDB(t, "reg-f02-a")

	err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		innerErr := repoA.WithinTx(ctx, func(inner context.Context) error {
			if err := repoA.Create(inner, &crossItem{ID: 202, Name: "failed-inner"}); err != nil {
				return err
			}
			return errors.NewCode(errors.InvalidInput, "abort inner")
		})
		require.Error(t, innerErr)
		// 外层吞掉内层错误继续提交——修复后必须失败。
		return nil
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))
	require.Equal(t, 0, countCrossRows(t, dbA, 202))
}

// TestRegressionF02_SavepointKeepsOuterWrites 验证保存点模式：内层失败只撤销
// 内层数据与内层回调，外层写入与外层回调保留。
func TestRegressionF02_SavepointKeepsOuterWrites(t *testing.T) {
	dbA, repoA := newCrossRepoDB(t, "reg-f02-sp")

	outerCallback := 0
	innerCallback := 0
	err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		require.NoError(t, repoA.Create(ctx, &crossItem{ID: 301, Name: "outer-before"}))
		require.NoError(t, contextx.AppendAfterCommit(ctx, func(context.Context) error {
			outerCallback++
			return nil
		}))

		spErr := repoA.WithinSavepoint(ctx, func(spx context.Context) error {
			if err := repoA.Create(spx, &crossItem{ID: 302, Name: "inner-failed"}); err != nil {
				return err
			}
			require.NoError(t, contextx.AppendAfterCommit(spx, func(context.Context) error {
				innerCallback++
				return nil
			}))
			return errors.NewCode(errors.InvalidInput, "abort savepoint scope")
		})
		require.Error(t, spErr) // 调用方显式决定继续外层

		require.NoError(t, repoA.Create(ctx, &crossItem{ID: 303, Name: "outer-after"}))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, countCrossRows(t, dbA, 301))
	require.Equal(t, 0, countCrossRows(t, dbA, 302))
	require.Equal(t, 1, countCrossRows(t, dbA, 303))
	require.Equal(t, 1, outerCallback)
	require.Equal(t, 0, innerCallback)
}

// TestRegressionF02_SavepointRequiresActiveTransaction 验证保存点作用域的
// 前置条件：不在事务中调用直接报错，不静默退化为无事务执行。
func TestRegressionF02_SavepointRequiresActiveTransaction(t *testing.T) {
	_, repoA := newCrossRepoDB(t, "reg-f02-pre")
	err := repoA.WithinSavepoint(context.Background(), func(context.Context) error {
		return nil
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))
}

// TestRegressionF02_SavepointIsolatesInnerWithinTxFailure 验证保存点的独立
// 失败边界：保存点内部经 WithinTx 失败（产生 rollback-only 标记）后，保存点
// 成功回滚即作废该标记——外层捕获局部错误后可以继续提交；进入保存点前
// 已有的失败状态则保持不变。
func TestRegressionF02_SavepointIsolatesInnerWithinTxFailure(t *testing.T) {
	dbA, repoA := newCrossRepoDB(t, "reg-f02-sp-boundary")

	// 场景一：保存点内的嵌套失败被局部撤销，外层继续提交。
	err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		require.NoError(t, repoA.Create(ctx, &crossItem{ID: 701, Name: "outer"}))
		spErr := repoA.WithinSavepoint(ctx, func(spx context.Context) error {
			return repoA.WithinTx(spx, func(inner context.Context) error {
				if err := repoA.Create(inner, &crossItem{ID: 702, Name: "inner-failed"}); err != nil {
					return err
				}
				return errors.NewCode(errors.InvalidInput, "abort nested")
			})
		})
		require.Error(t, spErr) // 调用方显式吞掉局部失败继续
		require.NoError(t, repoA.Create(ctx, &crossItem{ID: 703, Name: "outer-after"}))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, countCrossRows(t, dbA, 701))
	require.Equal(t, 0, countCrossRows(t, dbA, 702))
	require.Equal(t, 1, countCrossRows(t, dbA, 703))

	// 场景二：进入保存点前事务已被标记 rollback-only——保存点成功不得清除
	// 已有的失败状态，外层提交仍必须被拒绝。
	err = repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		innerErr := repoA.WithinTx(ctx, func(inner context.Context) error {
			return errors.NewCode(errors.InvalidInput, "abort outer-inner")
		})
		require.Error(t, innerErr) // 事务自此 rollback-only
		spErr := repoA.WithinSavepoint(ctx, func(spx context.Context) error {
			return repoA.Create(spx, &crossItem{ID: 704, Name: "sp-write"})
		})
		require.NoError(t, spErr)
		return nil
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))
	require.Equal(t, 0, countCrossRows(t, dbA, 704))
}

// TestRegressionF02_SavepointSwallowedInnerFailureDetected 验证：保存点 fn
// 返回 nil 但内部吞掉了嵌套 WithinTx 的失败（rollback-only 已置位、内层回调
// 已清除）时，保存点必须当作失败处理——物理回滚并返回错误，失败写入与
// 已清除的回调不得随外层提交造成数据与后续动作不一致。
func TestRegressionF02_SavepointSwallowedInnerFailureDetected(t *testing.T) {
	dbA, repoA := newCrossRepoDB(t, "reg-f02-sp-swallow")

	outerCallback := 0
	spCallback := 0
	err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		require.NoError(t, repoA.Create(ctx, &crossItem{ID: 801, Name: "outer"}))
		require.NoError(t, contextx.AppendAfterCommit(ctx, func(context.Context) error {
			outerCallback++
			return nil
		}))

		spErr := repoA.WithinSavepoint(ctx, func(spx context.Context) error {
			innerErr := repoA.WithinTx(spx, func(inner context.Context) error {
				if err := repoA.Create(inner, &crossItem{ID: 802, Name: "swallowed"}); err != nil {
					return err
				}
				return errors.NewCode(errors.InvalidInput, "abort nested")
			})
			require.Error(t, innerErr) // fn 吞掉内层失败
			require.NoError(t, contextx.AppendAfterCommit(spx, func(context.Context) error {
				spCallback++
				return nil
			}))
			return nil // 保存点 fn 返回 nil
		})
		// 修复后：检测到未处理的内部失败 → 物理回滚保存点并返回错误。
		require.Error(t, spErr)
		require.Equal(t, errors.FailedPrecondition, errors.Code(spErr))
		return nil // 外层吞掉保存点错误继续（数据已物理撤销）
	})
	require.NoError(t, err)
	require.Equal(t, 1, countCrossRows(t, dbA, 801))
	require.Equal(t, 0, countCrossRows(t, dbA, 802))
	require.Equal(t, 1, outerCallback)
	require.Equal(t, 0, spCallback)
}

// flakyReleaseSession 包装真实会话，按需注入 Release/RollbackTo 保存点故障。
// 显式转发 DatasourceID：包装层吞身份会被归属校验拒绝。
type flakyReleaseSession struct {
	orm.IOrmSession
	sp          orm.ISavepointSession
	datasourceID string
	failRelease bool
	failRollbackTo bool
}

func (s *flakyReleaseSession) DatasourceID() string { return s.datasourceID }

func (s *flakyReleaseSession) CreateSavepoint(ctx context.Context, name string) error {
	return s.sp.CreateSavepoint(ctx, name)
}

func (s *flakyReleaseSession) RollbackToSavepoint(ctx context.Context, name string) error {
	if s.failRollbackTo {
		return errors.NewCode(errors.Database, "injected rollback-to failure")
	}
	return s.sp.RollbackToSavepoint(ctx, name)
}

func (s *flakyReleaseSession) ReleaseSavepoint(ctx context.Context, name string) error {
	if s.failRelease {
		return errors.NewCode(errors.Database, "injected release failure")
	}
	return s.sp.ReleaseSavepoint(ctx, name)
}

// runFlakyReleaseSavepoint 在真实事务中用故障注入会话执行一次保存点写入。
// 返回（保存点错误、外层提交错误、失败写入残留行数）。
func runFlakyReleaseSavepoint(tb testing.TB, dbName string, failRollbackTo bool, writeID int64) (spErr, commitErr error, rows int) {
	tb.Helper()
	dbA, repoA := newCrossRepoDB(tb, dbName)

	scope, err := repoA.BeginTx(context.Background())
	require.NoError(tb, err)
	realSession, _, ok := orm.TxSessionFromContext(scope.Context())
	require.True(tb, ok)
	flaky := &flakyReleaseSession{
		IOrmSession:    realSession,
		sp:             realSession.(orm.ISavepointSession),
		datasourceID:   mustDatasourceID(tb, realSession),
		failRelease:    true,
		failRollbackTo: failRollbackTo,
	}
	// 从事务作用域派生 ctx：保存点内产生的 rollback-only 标记必须落在
	// 提交点读取的同一份 lifecycle 状态上。
	spCtx, err := orm.WithTxSession(scope.Context(), flaky, false)
	require.NoError(tb, err)

	spErr = repoA.WithinSavepoint(spCtx, func(spx context.Context) error {
		return repoA.Create(spx, &crossItem{ID: writeID, Name: "in-savepoint"})
	})
	commitErr = repoA.Commit(scope)
	rows = countCrossRows(tb, dbA, writeID)
	return spErr, commitErr, rows
}

func mustDatasourceID(tb testing.TB, engine orm.IOrm) string {
	tb.Helper()
	id, ok := orm.DatasourceIDOf(engine)
	require.True(tb, ok)
	return id
}

// TestRegressionF02_SavepointReleaseFailureUndoesScope 验证：释放保存点失败时
// 必须尝试物理撤销本层写入与回调——撤销成功则外层可继续且无残留；
// 撤销也无法确认成功时整体 rollback-only，外层提交被拒绝。
func TestRegressionF02_SavepointReleaseFailureUndoesScope(t *testing.T) {
	// 场景一：Release 失败但回滚保存点成功 → 局部撤销完成，外层提交成功、失败写入为 0。
	spErr, commitErr, rows := runFlakyReleaseSavepoint(t, "reg-f02-sp-rel-1", false, 811)
	require.Error(t, spErr)
	require.NoError(t, commitErr)
	require.Equal(t, 0, rows)

	// 场景二：Release 与 RollbackTo 都失败 → 无法确认撤销，整体 rollback-only，
	// 外层提交被拒绝，失败写入同样不落库。
	spErr2, commitErr2, rows2 := runFlakyReleaseSavepoint(t, "reg-f02-sp-rel-2", true, 812)
	require.Error(t, spErr2)
	require.Error(t, commitErr2)
	require.Equal(t, errors.FailedPrecondition, errors.Code(commitErr2))
	require.Equal(t, 0, rows2)
}

// maskedDatabase 隐藏 DatasourceID（模拟只转发接口方法的包装层）。
type maskedDatabase struct{ db.IDatabase }

// TestRegressionF01_IdentityHidingIsRejected 验证：任一侧无法提供数据源身份
// （例如用包装器把 DatasourceID 藏起来）时，事务传播被拒绝而不是静默放行。
func TestRegressionF01_IdentityHidingIsRejected(t *testing.T) {
	_, repoA := newCrossRepoDB(t, "reg-f01-mask-a")

	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: "file:reg-f01-mask-b?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(context.Background(),
		"CREATE TABLE items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0, name TEXT)")
	require.NoError(t, err)
	engine, err := lite.New(maskedDatabase{IDatabase: database})
	require.NoError(t, err)
	repoB, err := NewRepo[*crossItem, int64](engine, "items")
	require.NoError(t, err)

	err = repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		return repoB.Create(ctx, &crossItem{ID: 111, Name: "masked"})
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))

	var n int
	row := database.QueryRow(context.Background(), "SELECT COUNT(*) FROM items WHERE id = 111")
	require.NoError(t, row.Scan(&n))
	require.Equal(t, 0, n)
}

// TestRegressionF01_CrossSourceAuditStoreRejected 验证：审计存储复用异源
// 事务会话同样被拒绝，审计流水不会写进别的数据源。
func TestRegressionF01_CrossSourceAuditStoreRejected(t *testing.T) {
	dbA, repoA := newCrossRepoDB(t, "reg-f01-audit-a")

	auditDDL := `CREATE TABLE audits (
		id INTEGER PRIMARY KEY, resource_kind TEXT, entity_id TEXT, operation TEXT,
		operator TEXT, timestamp DATETIME, changes TEXT, metadata TEXT)`
	_, err := dbA.Exec(context.Background(), auditDDL)
	require.NoError(t, err)

	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: "file:reg-f01-audit-b?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(context.Background(), auditDDL)
	require.NoError(t, err)
	engine, err := lite.New(database)
	require.NoError(t, err)
	sink, err := NewAuditStore(engine, "audits", fixedGenerator{})
	require.NoError(t, err)

	err = repoA.WithinTx(context.Background(), func(ctx context.Context) error {
		_, err := sink.SaveAuditRecord(ctx, auditRecordForTest())
		return err
	})
	require.Error(t, err)
	require.Equal(t, errors.FailedPrecondition, errors.Code(err))

	for _, target := range []db.IDatabase{dbA, database} {
		var n int
		row := target.QueryRow(context.Background(), "SELECT COUNT(*) FROM audits")
		require.NoError(t, row.Scan(&n))
		require.Equal(t, 0, n)
	}
}

type fixedGenerator struct{}

func (fixedGenerator) Next() (int64, error) { return 1, nil }

func auditRecordForTest() audited.AuditRecord {
	return audited.AuditRecord{
		EntityID:  "10",
		Operation: audited.AuditOpCreate,
		Operator:  "user",
	}
}

// TestRegressionF02_InnerPanicMarksRollbackOnly 验证：内层作用域 panic 且
// 外层 recover 后继续，失败写入不得落库。默认嵌套模式额外要求提交被拒绝；
// 保存点模式的局部回滚成功后外层可以继续提交（这是调用方的显式选择），
// 但内层写入同样不得残留。
func TestRegressionF02_InnerPanicMarksRollbackOnly(t *testing.T) {
	for _, mode := range []string{"nested", "savepoint"} {
		t.Run(mode, func(t *testing.T) {
			dbA, repoA := newCrossRepoDB(t, "reg-f02-panic-"+mode)
			id := int64(500)
			err := repoA.WithinTx(context.Background(), func(ctx context.Context) error {
				func() {
					defer func() { _ = recover() }()
					inner := func(innerCtx context.Context) error {
						require.NoError(t, repoA.Create(innerCtx, &crossItem{ID: id, Name: mode}))
						panic("abort inner")
					}
					if mode == "nested" {
						_ = repoA.WithinTx(ctx, inner)
					} else {
						_ = repoA.WithinSavepoint(ctx, inner)
					}
				}()
				return nil
			})
			if mode == "nested" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 0, countCrossRows(t, dbA, id))
		})
	}
}
