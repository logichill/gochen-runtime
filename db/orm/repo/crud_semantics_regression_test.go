package repo

import (
	"context"
	"testing"
	"time"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/db"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

// semanticsItem 是 F03 回归用的非审计实体（嵌入 crud.Entity，具备可写版本字段）。
type semanticsItem struct {
	crud.Entity[int64]
	Name string `json:"name"`
}

// semanticsAuditedItem 是 F04 回归用的审计实体。
type semanticsAuditedItem struct {
	audited.AuditedEntity[int64]
	Name string `json:"name"`
}

func newSemanticsRepo(tb testing.TB, name string) (db.IDatabase, *Repo[*semanticsItem, int64]) {
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
	repository, err := NewRepo[*semanticsItem, int64](engine, "items")
	require.NoError(tb, err)
	return database, repository
}

func newSemanticsAuditedRepo(tb testing.TB, name string) (db.IDatabase, *Repo[*semanticsAuditedItem, int64]) {
	tb.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: "file:" + name + "?mode=memory&cache=shared",
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(context.Background(), `CREATE TABLE audited_items (
		id INTEGER PRIMARY KEY,
		version INTEGER NOT NULL DEFAULT 0,
		name TEXT,
		created_at DATETIME,
		created_by TEXT,
		updated_at DATETIME,
		updated_by TEXT,
		deleted_at DATETIME,
		deleted_by TEXT)`)
	require.NoError(tb, err)
	engine, err := lite.New(database)
	require.NoError(tb, err)
	repository, err := NewRepo[*semanticsAuditedItem, int64](engine, "audited_items")
	require.NoError(tb, err)
	return database, repository
}

// TestRegressionF03_OptimisticLockAppliesWithoutAudit 验证：未启用审计的普通
// CRUD 实体（可写版本字段）同样受乐观锁保护——同一版本只有一次更新成功，
// 旧版本写入返回并发冲突；更新不存在的记录返回 NotFound。
func TestRegressionF03_OptimisticLockAppliesWithoutAudit(t *testing.T) {
	_, repo := newSemanticsRepo(t, "reg-f03")

	require.NoError(t, repo.Create(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 1}, Name: "initial",
	}))

	left, err := repo.Get(context.Background(), 1)
	require.NoError(t, err)
	right, err := repo.Get(context.Background(), 1)
	require.NoError(t, err)

	left.Name = "first-writer"
	require.NoError(t, repo.Update(context.Background(), left))

	right.Name = "stale-writer"
	err = repo.Update(context.Background(), right)
	require.Error(t, err)
	require.Equal(t, errors.Concurrency, errors.Code(err))

	stored, err := repo.Get(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "first-writer", stored.Name)
	require.Equal(t, uint64(1), stored.GetVersion())

	err = repo.Update(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 999, Version: 0}, Name: "missing",
	})
	require.Error(t, err)
	require.Equal(t, errors.NotFound, errors.Code(err))
}

// TestRegressionF03_WithoutVersioningEscapeHatch 验证显式退出版本保护：
// 不施加版本条件、不推进版本（旧值覆盖），但目标不存在仍返回 NotFound。
func TestRegressionF03_WithoutVersioningEscapeHatch(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: "file:reg-f03-nv?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(context.Background(),
		"CREATE TABLE items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0, name TEXT)")
	require.NoError(t, err)
	engine, err := lite.New(database)
	require.NoError(t, err)
	repo, err := NewRepo[*semanticsItem, int64](engine, "items", WithoutVersioning[*semanticsItem, int64]())
	require.NoError(t, err)

	require.NoError(t, repo.Create(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 7}, Name: "v1",
	}))
	first, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)
	second, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)

	// 退出版本保护后，同版本先后更新不再冲突（旧值覆盖由调用方自行承担）。
	first.Name = "first-writer"
	require.NoError(t, repo.Update(context.Background(), first))
	second.Name = "stale-writer"
	require.NoError(t, repo.Update(context.Background(), second))
	stored, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "stale-writer", stored.Name)

	err = repo.Update(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 4242}, Name: "missing",
	})
	require.Error(t, err)
	require.Equal(t, errors.NotFound, errors.Code(err))
}

// TestRegressionF04_CreateAttributionIsServerControlled 验证：审计实体的创建
// 归因、删除态与初始版本由服务端控制，客户端提供的受保护字段一律被覆写。
func TestRegressionF04_CreateAttributionIsServerControlled(t *testing.T) {
	dbAudit, repo := newSemanticsAuditedRepo(t, "reg-f04")

	ctx, err := contextx.WithOperator(context.Background(), "authenticated-user")
	require.NoError(t, err)
	forgedAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	entity := &semanticsAuditedItem{Name: "submitted"}
	entity.ID = 404
	entity.CreatedBy = "forged-admin"
	entity.CreatedAt = forgedAt
	entity.DeletedAt = &forgedAt
	entity.SetVersion(99)

	require.NoError(t, repo.Create(ctx, entity))

	stored, err := repo.Get(context.Background(), 404)
	require.NoError(t, err)
	require.Equal(t, "authenticated-user", stored.GetCreatedBy())
	require.Equal(t, "authenticated-user", stored.GetUpdatedBy())
	require.False(t, stored.CreatedAt.Equal(forgedAt))
	require.Nil(t, stored.GetDeletedAt())
	// 初始版本受服务端控制：创建后的版本从 1 起步（SetUpdatedInfo 推进一次）。
	require.Equal(t, uint64(1), stored.GetVersion())

	var dbVersion uint64
	row := dbAudit.QueryRow(context.Background(), "SELECT version FROM audited_items WHERE id = 404")
	require.NoError(t, row.Scan(&dbVersion))
	require.Equal(t, uint64(1), dbVersion)
}

// TestRegressionF03_FailedUpdateKeepsEntityVersion 验证：更新失败（并发冲突）
// 后实体版本保持进入时的值——用同一对象直接重试不得"恰好"命中数据库里的
// 新版本并覆盖别人的写入。
func TestRegressionF03_FailedUpdateKeepsEntityVersion(t *testing.T) {
	_, repo := newSemanticsRepo(t, "reg-f03-retry")

	require.NoError(t, repo.Create(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 20}, Name: "initial",
	}))
	left, err := repo.Get(context.Background(), 20)
	require.NoError(t, err)
	right, err := repo.Get(context.Background(), 20)
	require.NoError(t, err)

	left.Name = "writer"
	require.NoError(t, repo.Update(context.Background(), left))

	// right 是过期快照（版本 0，库里已是 1）。
	require.Equal(t, uint64(0), right.GetVersion())
	right.Name = "stale"
	err = repo.Update(context.Background(), right)
	require.Error(t, err)
	require.Equal(t, errors.Concurrency, errors.Code(err))
	require.Equal(t, uint64(0), right.GetVersion()) // 版本未被失败更新推进

	// 直接重试同一对象：仍然冲突，而不是覆盖 writer 的结果。
	err = repo.Update(context.Background(), right)
	require.Error(t, err)
	require.Equal(t, errors.Concurrency, errors.Code(err))

	stored, err := repo.Get(context.Background(), 20)
	require.NoError(t, err)
	require.Equal(t, "writer", stored.Name)
	require.Equal(t, uint64(1), stored.GetVersion())
}

// TestRegressionF03_ConstrainedUpdateAdvancesVersion 验证：受约束更新与普通
// 更新同版本口径——非审计实体同样推进版本，两个持有同一版本的成功更新
// 不允许都命中（后者按约束写口径返回 Conflict，见 classifyConstraintWriteMiss）。
func TestRegressionF03_ConstrainedUpdateAdvancesVersion(t *testing.T) {
	dbItems, repo := newSemanticsRepo(t, "reg-f03-constrained")
	require.NoError(t, repo.Create(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 30}, Name: "initial",
	}))
	_, err := dbItems.Exec(context.Background(), "UPDATE items SET version = 1 WHERE id = 30")
	require.NoError(t, err)

	constraint := scoped.Allow(scoped.Resource{Kind: "items", ID: "30", Revision: "1"}).WriteConstraint()
	first := &semanticsItem{Entity: crud.Entity[int64]{ID: 30, Version: 1}, Name: "writer1"}
	second := &semanticsItem{Entity: crud.Entity[int64]{ID: 30, Version: 1}, Name: "writer2"}

	require.NoError(t, repo.UpdateWithConstraint(context.Background(), first, constraint))
	err = repo.UpdateWithConstraint(context.Background(), second, constraint)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Conflict) || errors.Is(err, errors.Concurrency),
		"stale constrained update must fail")

	stored, err := repo.Get(context.Background(), 30)
	require.NoError(t, err)
	require.Equal(t, "writer1", stored.Name)
	require.Equal(t, uint64(2), stored.GetVersion())
}

// TestRegressionF03_ConstrainedUpdateOnZeroVersionSucceeds 验证：未声明
// WithScope 的普通仓储（版本字段解析与范围过滤解耦后）对新建实体（版本 0）
// 以 revision "0" 执行受约束更新必须成功，而不是在版本对齐上报内部错误。
func TestRegressionF03_ConstrainedUpdateOnZeroVersionSucceeds(t *testing.T) {
	_, repo := newSemanticsRepo(t, "reg-f03-zero-version")

	require.NoError(t, repo.Create(context.Background(), &semanticsItem{
		Entity: crud.Entity[int64]{ID: 40}, Name: "initial",
	}))
	stored, err := repo.Get(context.Background(), 40)
	require.NoError(t, err)
	require.Equal(t, uint64(0), stored.GetVersion())

	constraint := scoped.Allow(scoped.Resource{Kind: "items", ID: "40", Revision: "0"}).WriteConstraint()
	stored.Name = "constrained"
	require.NoError(t, repo.UpdateWithConstraint(context.Background(), stored, constraint))

	after, err := repo.Get(context.Background(), 40)
	require.NoError(t, err)
	require.Equal(t, "constrained", after.Name)
	require.Equal(t, uint64(1), after.GetVersion())
}
