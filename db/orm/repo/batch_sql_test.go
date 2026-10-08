package repo

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/auth/scoped"
	"gochen/db"
	"gochen/testkit/require"
)

func countStatements(stmts []capturedStatement, fragment string) int {
	n := 0
	for _, stmt := range stmts {
		if strings.Contains(normalizeSQL(stmt.SQL), fragment) {
			n++
		}
	}
	return n
}

func TestChunkSlice(t *testing.T) {
	require.Equal(t, [][]int(nil), chunkSlice([]int{}, 3))
	require.Equal(t, [][]int{{1, 2}}, chunkSlice([]int{1, 2}, 3))
	require.Equal(t, [][]int{{1, 2, 3}}, chunkSlice([]int{1, 2, 3}, 3))
	require.Equal(t, [][]int{{1, 2, 3}, {4, 5}}, chunkSlice([]int{1, 2, 3, 4, 5}, 3))
	// size <= 0 不分片，避免除零/死循环。
	require.Equal(t, [][]int{{1, 2, 3}}, chunkSlice([]int{1, 2, 3}, 0))
}

// 约束批量创建必须合并成一条多值 INSERT。
//
// 回归点：启用 L3 后批量创建曾退化成 N 条单行 INSERT——同一批数据仅仅因为
// 开了安全档，写入的语句条数就翻了 N 倍，而无约束路径一直是一条。
func TestSQLCapture_CreateAll_ConstrainedPathUsesSingleInsert(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	provider := scoped.SingleEntityConstraint("capture_entities", scoped.WriteConstraint{
		Resources: []scoped.ResourceConstraint{
			{Kind: "capture_entities", ResourceID: "1", TenantID: "tenant-a", ManagedScopeID: 101},
			{Kind: "capture_entities", ResourceID: "2", TenantID: "tenant-a", ManagedScopeID: 101},
			{Kind: "capture_entities", ResourceID: "3", TenantID: "tenant-a", ManagedScopeID: 101},
		},
	})
	ctx := scoped.WithConstraint(scopedContext(t, "tenant-a", 101), provider)

	capturing.reset()
	entities := []*captureEntity{{ID: 1, Name: "n1"}, {ID: 2, Name: "n2"}, {ID: 3, Name: "n3"}}
	require.NoError(t, repository.CreateAll(ctx, entities))

	require.Equal(t, 1,
		countStatements(capturing.captured(), `INSERT INTO "capture_entities"`),
		"约束批量创建应合并成一条多值 INSERT")

	// 仅数语句条数不足以证明合并成功——一条 INSERT 也可能只写进去一行。
	// captureEntity 有 6 列，3 行即 18 个占位参数。
	for _, stmt := range capturing.captured() {
		if strings.Contains(normalizeSQL(stmt.SQL), `INSERT INTO "capture_entities"`) {
			require.Equal(t, 18, len(stmt.Args), "一条 INSERT 应携带全部 3 行")
		}
	}

	// 合并不得牺牲逐条盖戳：每条实体仍要带上自己被授权的租户与范围。
	for _, entity := range entities {
		require.Equal(t, "tenant-a", entity.TenantID)
		require.Equal(t, int64(101), entity.ManagedScopeID)
	}
}

// 多值 INSERT 同样受占位参数上限约束：合并成一条不等于可以无限合并。
//
// 实际分片由 ORM 适配层按真实列数完成（lite 的 model.createMaxParams）；本用例
// 守住仓储层看得见的不变量——约束批量创建走合并路径后，仍没有任何一条语句越限。
func TestSQLCapture_CreateAll_RespectsBindParameterLimit(t *testing.T) {
	repository, capturing := newCaptureRepo(t)

	// captureEntity 有 6 个可插入列 → 每条 INSERT 最多 MaxBindParameters/6 行。
	const columns = 6
	total := dialect.DefaultMaxBindParameters/columns + 1

	resources := make([]scoped.ResourceConstraint, 0, total)
	entities := make([]*captureEntity, 0, total)
	for i := 1; i <= total; i++ {
		id := strconv.Itoa(i)
		resources = append(resources, scoped.ResourceConstraint{
			Kind: "capture_entities", ResourceID: id, TenantID: "tenant-a", ManagedScopeID: 101,
		})
		entities = append(entities, &captureEntity{ID: int64(i), Name: "n" + id})
	}
	ctx := scoped.WithConstraint(scopedContext(t, "tenant-a", 101),
		scoped.SingleEntityConstraint("capture_entities", scoped.WriteConstraint{Resources: resources}))

	capturing.reset()
	require.NoError(t, repository.CreateAll(ctx, entities))

	require.Equal(t, 2,
		countStatements(capturing.captured(), `INSERT INTO "capture_entities"`),
		"越过上限一行即应切成 2 条 INSERT")
	for _, stmt := range capturing.captured() {
		require.True(t, len(stmt.Args) <= dialect.DefaultMaxBindParameters,
			"单条 INSERT 的占位参数不得超过 dialect.DefaultMaxBindParameters")
	}
}

// 配置的占位参数预算必须真的抵达分片逻辑。
//
// 这条守的是 DBConfig → IDatabase → IOrm → Repo 这条探测链：链上任何一环
// 断了都不会报错，只会静默退回 30000 的默认预算——而配置这个值的人恰恰是
// 因为环境撑不住 30000 才配的。
func TestSQLCapture_ConfiguredBindLimitReachesChunking(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:            "sqlite",
		Database:          ":memory:",
		MaxOpenConns:      1,
		MaxIdleConns:      1,
		MaxBindParameters: 100,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(),
		"CREATE TABLE budget_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '')")
	require.NoError(t, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(t, err)
	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "budget_entities")
	require.NoError(t, err)

	ids := make([]int64, 0, 250)
	for i := 1; i <= 250; i++ {
		ids = append(ids, int64(i))
	}

	capturing.reset()
	require.NoError(t, repository.DeleteAll(context.Background(), ids))

	// 预算 100 → 250 个 ID 切成 3 条；用默认 30000 时会是 1 条。
	require.Equal(t, 3,
		countStatements(capturing.captured(), `DELETE FROM "budget_entities"`),
		"配置的预算未抵达分片逻辑（探测链断了）")
	for _, stmt := range capturing.captured() {
		require.True(t, len(stmt.Args) <= 100, "单条语句不得超过配置的预算")
	}
}

// 超过占位参数上限的 ID 列表必须分片下发，而不是拼成一条超长 IN。
func TestSQLCapture_DeleteAll_ChunksLargeIDList(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(),
		"CREATE TABLE chunk_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '')")
	require.NoError(t, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(t, err)
	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "chunk_entities")
	require.NoError(t, err)

	// 刚好越过占位参数上限 → 必须切成 2 条；用常量表达以免上限调整后断言失真。
	total := dialect.DefaultMaxBindParameters + 1
	ids := make([]int64, 0, total)
	for i := 1; i <= total; i++ {
		ids = append(ids, int64(i))
	}

	capturing.reset()
	require.NoError(t, repository.DeleteAll(context.Background(), ids))

	require.Equal(t, 2,
		countStatements(capturing.captured(), `DELETE FROM "chunk_entities"`),
		"超出占位参数上限的 ID 列表必须分片下发")
	for _, stmt := range capturing.captured() {
		require.True(t, len(stmt.Args) <= dialect.DefaultMaxBindParameters,
			"单条语句的占位参数不得超过 dialect.DefaultMaxBindParameters")
	}
}

// 超过占位参数上限的 ID 列表在 ListByIds 上同样必须分片下发。
//
// 回归 R2b-F02：ListByIds 曾经把整份 ID 列表塞进单条 `id IN (...)`，
// ID 数量一过驱动的变量上限（SQLite 32766）整次查询直接报
// "too many SQL variables"，而同仓储的 DeleteAll 早已分片。
func TestSQLCapture_ListByIds_ChunksLargeIDList(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(),
		"CREATE TABLE list_by_ids_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '')")
	require.NoError(t, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(t, err)
	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "list_by_ids_entities")
	require.NoError(t, err)

	// 刚好越过占位参数上限 → 必须切成 2 条；用常量表达以免上限调整后断言失真。
	total := dialect.DefaultMaxBindParameters + 1
	ids := make([]int64, 0, total)
	for i := 1; i <= total; i++ {
		ids = append(ids, int64(i))
	}

	capturing.reset()
	entities, err := repository.ListByIds(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, entities, 0)

	require.Equal(t, 2,
		countStatements(capturing.captured(), `SELECT * FROM "list_by_ids_entities"`),
		"超出占位参数上限的 ID 列表必须分片下发")
	for _, stmt := range capturing.captured() {
		require.True(t, len(stmt.Args) <= dialect.DefaultMaxBindParameters,
			"单条语句的占位参数不得超过 dialect.DefaultMaxBindParameters")
	}
}

// 分片结果必须包含所有命中行，且重复 ID 不得放大结果；ListByIds 不承诺顺序。
func TestSQLCapture_ListByIds_MergesChunksWithoutDuplicates(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:            "sqlite",
		Database:          ":memory:",
		MaxOpenConns:      1,
		MaxIdleConns:      1,
		MaxBindParameters: 100,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(), `
		CREATE TABLE ordered_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '');
		INSERT INTO ordered_entities (id, name)
		WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < 250)
		SELECT x, 'n' FROM seq;
	`)
	require.NoError(t, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(t, err)
	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "ordered_entities")
	require.NoError(t, err)

	// 预算 100 → 250 个唯一 ID 切成 3 条；重复 ID 先折叠，不放大结果集。
	ids := make([]int64, 0, 253)
	for i := 1; i <= 250; i++ {
		ids = append(ids, int64((i*73)%250+1))
	}
	ids = append(ids, 5, 5, 7)

	capturing.reset()
	entities, err := repository.ListByIds(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, entities, 250, "重复 ID 不得被分片放大成多行")

	require.Equal(t, 3,
		countStatements(capturing.captured(), `SELECT * FROM "ordered_entities"`),
		"预算 100 下 250 个唯一 ID 应切成 3 条")
	gotIDs := make([]int64, len(entities))
	for i, entity := range entities {
		gotIDs[i] = entity.ID
	}
	require.ElementsMatch(t, ids[:250], gotIDs)
}

// SQLite 端到端：40000 个 ID 必须成功返回全部命中行。
//
// 修复前 40000 个绑定参数超过 modernc sqlite 的 32766 变量上限，整次调用
// 直接以 Database 错误失败；分片后切成 30000 + 10000 两条语句。
func TestListByIds_ChunksBeyondSQLiteVariableLimit(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(), `
		CREATE TABLE wide_id_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '');
		INSERT INTO wide_id_entities (id, name)
		WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < 40000)
		SELECT x, 'n' FROM seq;
	`)
	require.NoError(t, err)

	ormEngine, err := lite.New(database)
	require.NoError(t, err)
	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "wide_id_entities")
	require.NoError(t, err)

	ids := make([]int64, 40000)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	entities, err := repository.ListByIds(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, entities, 40000)
	require.Equal(t, int64(1), entities[0].ID)
	require.Equal(t, int64(40000), entities[len(entities)-1].ID)
}

type smallBudgetDB struct{ db.IDatabase }

func (smallBudgetDB) MaxBindParameters() int { return 100 }

func TestListByIDsReservesTenantAndScopeParameters(t *testing.T) {
	_, capture := newCaptureRepo(t)
	engine, err := lite.New(smallBudgetDB{capture})
	require.NoError(t, err)
	r, err := NewRepo[*captureEntity, int64](engine, "capture_entities",
		WithIsolation[*captureEntity, int64](IsolationCols{Column: "tenant_id"}),
		WithScope[*captureEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id"}))
	require.NoError(t, err)
	ids := make([]int64, 198)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	capture.reset()
	_, err = r.ListByIds(scopedContext(t, "tenant-a", 101, 102), ids)
	require.NoError(t, err)
	statements := capture.captured()
	require.Equal(t, 3, len(statements))
	for _, stmt := range statements {
		require.True(t, len(stmt.Args) <= 100, "scope and ID parameters must share the budget")
	}
}
