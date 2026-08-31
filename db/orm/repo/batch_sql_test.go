package repo

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/auth/scoped"
	"gochen/db"
	"gochen/db/dialect"
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
