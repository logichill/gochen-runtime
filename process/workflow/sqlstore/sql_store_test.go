package sqlstore

import (
	"context"
	"testing"
	"time"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/errors"
	"gochen/process/workflow"
	"gochen/testkit/require"

	_ "modernc.org/sqlite"
)

func setupStore(t *testing.T) (*SQLStore, db.IDatabase) {
	t.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     t.TempDir() + "/workflow.db",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	store, err := NewSQLStore(database, nil)
	require.NoError(t, err)
	require.NoError(t, store.CreateTables(context.Background()))
	return store, database
}

func TestNewSQLStore_Validation(t *testing.T) {
	_, err := NewSQLStore(nil, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     t.TempDir() + "/v.db",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// 表名来自配置，必须挡住 SQL 片段注入。
	_, err = NewSQLStore(database, &Config{InstanceTable: "bad name; DROP TABLE x"})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestSQLStore_DefinitionRoundTripAndVersioning(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)

	v1 := &workflow.Definition{
		ID:          "approval",
		Version:     1,
		Name:        "审批流",
		StartNodeID: "review",
		Nodes: []workflow.Node{
			{ID: "review", Timeout: 30 * time.Minute, Edges: []workflow.Edge{
				workflow.NewConditionalEdge("vip", "amount > 1000"),
				workflow.NewDefaultEdge("normal"),
			}},
			{ID: "vip"},
			{ID: "normal"},
		},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, store.SaveDefinition(ctx, v1))

	// 不存在的定义返回 nil，而不是错误。
	missing, err := store.GetDefinition(ctx, "nope", 0)
	require.NoError(t, err)
	require.Nil(t, missing)

	loaded, err := store.GetDefinition(ctx, "approval", 1)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "审批流", loaded.Name)
	require.Equal(t, uint32(1), loaded.Version)
	require.Len(t, loaded.Nodes, 3)
	// 图结构与条件、超时都必须完整往返。
	require.Equal(t, 30*time.Minute, loaded.Nodes[0].Timeout)
	require.Equal(t, "amount > 1000", loaded.Nodes[0].Edges[0].Condition)
	require.True(t, loaded.Nodes[0].Edges[1].Default)

	// 写入 v2 后，version == 0 应取最新版本，指定版本仍能取回旧版本。
	v2 := &workflow.Definition{
		ID: "approval", Version: 2, Name: "审批流v2", StartNodeID: "review",
		Nodes: []workflow.Node{{ID: "review"}},
	}
	require.NoError(t, store.SaveDefinition(ctx, v2))

	latest, err := store.GetDefinition(ctx, "approval", 0)
	require.NoError(t, err)
	require.Equal(t, uint32(2), latest.Version)

	old, err := store.GetDefinition(ctx, "approval", 1)
	require.NoError(t, err)
	require.Equal(t, uint32(1), old.Version)

	// 删除单版本不影响其他版本。
	require.NoError(t, store.DeleteDefinition(ctx, "approval", 1))
	old, err = store.GetDefinition(ctx, "approval", 1)
	require.NoError(t, err)
	require.Nil(t, old)
	latest, err = store.GetDefinition(ctx, "approval", 0)
	require.NoError(t, err)
	require.NotNil(t, latest)

	// version == 0 删除所有版本。
	require.NoError(t, store.DeleteDefinition(ctx, "approval", 0))
	latest, err = store.GetDefinition(ctx, "approval", 0)
	require.NoError(t, err)
	require.Nil(t, latest)
}

func TestSQLStore_SaveDefinitionRequiresVersion(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)

	err := store.SaveDefinition(ctx, &workflow.Definition{ID: "x", StartNodeID: "a"})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func newTestState(id workflow.ID) *workflow.State {
	now := time.Now().UTC().Truncate(time.Second)
	return &workflow.State{
		ID:                  id,
		DefinitionID:        "approval",
		DefinitionVersion:   1,
		Status:              workflow.InstanceStatusRunning,
		ActiveNodeIDs:       []string{"review"},
		ActiveNodeStartedAt: map[string]time.Time{"review": now},
		PendingJoins:        []workflow.PendingJoin{},
		CompletedNodeIDs:    []string{"submit"},
		History:             []workflow.HistoryEntry{{Action: "start", NodeID: "review", At: now}},
		Data:                map[string]any{"amount": float64(1500)},
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

func TestSQLStore_InstanceOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)

	// 不存在的实例返回 nil。
	got, err := store.Get(ctx, "absent")
	require.NoError(t, err)
	require.Nil(t, got)

	st := newTestState("wf-1")
	require.NoError(t, store.SaveIfVersion(ctx, st, 0))
	require.Equal(t, uint64(1), st.Version)

	// 首次写入是去重闸门：重复以 expectedVersion == 0 写入必须冲突。
	dup := newTestState("wf-1")
	err = store.SaveIfVersion(ctx, dup, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Conflict))

	loaded, err := store.Get(ctx, "wf-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), loaded.Version)
	require.Equal(t, []string{"review"}, loaded.ActiveNodeIDs)
	require.Equal(t, []string{"submit"}, loaded.CompletedNodeIDs)
	require.Equal(t, float64(1500), loaded.Data["amount"])
	require.Len(t, loaded.History, 1)

	// 版本匹配的更新成功并递增版本。
	loaded.ActiveNodeIDs = []string{"vip"}
	require.NoError(t, store.SaveIfVersion(ctx, loaded, 1))
	require.Equal(t, uint64(2), loaded.Version)

	// 用过期版本再写必须冲突（模拟另一个进程已推进过实例）。
	stale := newTestState("wf-1")
	err = store.SaveIfVersion(ctx, stale, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Conflict))

	// 不存在的实例用非零版本更新应报 NotFound，而不是 Conflict。
	err = store.SaveIfVersion(ctx, newTestState("ghost"), 3)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.NotFound))

	require.NoError(t, store.Delete(ctx, "wf-1"))
	got, err = store.Get(ctx, "wf-1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestSQLStore_SaveOverwrites(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)

	st := newTestState("wf-save")
	require.NoError(t, store.Save(ctx, st))
	require.Equal(t, uint64(1), st.Version)

	st.Status = workflow.InstanceStatusCompleted
	require.NoError(t, store.Save(ctx, st))

	loaded, err := store.Get(ctx, "wf-save")
	require.NoError(t, err)
	require.Equal(t, workflow.InstanceStatusCompleted, loaded.Status)
}

func TestSQLStore_ListInstances(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)
	base := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)

	older := newTestState("older")
	older.ActiveNodeStartedAt = map[string]time.Time{"review": base}
	require.NoError(t, store.SaveIfVersion(ctx, older, 0))

	newer := newTestState("newer")
	newer.ActiveNodeStartedAt = map[string]time.Time{"review": base.Add(2 * time.Hour)}
	require.NoError(t, store.SaveIfVersion(ctx, newer, 0))

	done := newTestState("done")
	done.Status = workflow.InstanceStatusCompleted
	done.ActiveNodeIDs = []string{}
	done.ActiveNodeStartedAt = map[string]time.Time{}
	require.NoError(t, store.SaveIfVersion(ctx, done, 0))

	other := newTestState("other")
	other.DefinitionID = "another"
	other.ActiveNodeStartedAt = map[string]time.Time{"review": base.Add(time.Hour)}
	require.NoError(t, store.SaveIfVersion(ctx, other, 0))

	// 状态过滤 + 最早活动优先排序。
	running, err := store.ListInstances(ctx, workflow.InstanceQuery{
		Statuses: []workflow.InstanceStatus{workflow.InstanceStatusRunning},
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, running, 3)
	require.Equal(t, workflow.ID("older"), running[0].ID)

	// 定义过滤。
	byDef, err := store.ListInstances(ctx, workflow.InstanceQuery{DefinitionID: "another", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byDef, 1)
	require.Equal(t, workflow.ID("other"), byDef[0].ID)

	// ActiveSince 只保留活动时刻不晚于阈值的实例，这是超时巡检走索引的关键条件。
	// 阈值取 base 本身，同时验证边界是包含关系。
	stale, err := store.ListInstances(ctx, workflow.InstanceQuery{
		Statuses:    []workflow.InstanceStatus{workflow.InstanceStatusRunning},
		ActiveSince: base,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, workflow.ID("older"), stale[0].ID)

	// Limit 生效，且非法 limit 必须被拒绝。
	limited, err := store.ListInstances(ctx, workflow.InstanceQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, limited, 2)

	_, err = store.ListInstances(ctx, workflow.InstanceQuery{Limit: 0})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// TestSQLStore_EngineEndToEnd 用真实 SQL 存储驱动引擎全流程，
// 并在"重启"（丢弃引擎、仅保留数据库）后验证实例可恢复继续推进。
func TestSQLStore_EngineEndToEnd(t *testing.T) {
	ctx := context.Background()
	store, database := setupStore(t)

	engine := workflow.NewEngine(store)
	def := &workflow.Definition{
		ID:          "order",
		StartNodeID: "review",
		Nodes: []workflow.Node{
			{ID: "review", Edges: []workflow.Edge{
				workflow.NewConditionalEdge("vip", "amount > 1000"),
				workflow.NewDefaultEdge("normal"),
			}},
			{ID: "vip", Edges: []workflow.Edge{workflow.NewEdge("done")}},
			{ID: "normal", Edges: []workflow.Edge{workflow.NewEdge("done")}},
			{ID: "done", Kind: workflow.NodeKindTask},
		},
	}
	require.NoError(t, engine.SaveDefinition(ctx, def))
	require.Equal(t, uint32(1), def.Version)

	require.NoError(t, engine.CreateInstanceWithData(ctx, "o-1", "order", map[string]any{"amount": 5000}))
	require.NoError(t, engine.StartInstance(ctx, "o-1"))
	require.NoError(t, engine.AdvanceNode(ctx, "o-1", "review"))

	st, err := store.Get(ctx, "o-1")
	require.NoError(t, err)
	require.Equal(t, []string{"vip"}, st.ActiveNodeIDs)

	// 模拟进程重启：新建 store 与 engine，只共享同一个数据库。
	restarted, err := NewSQLStore(database, nil)
	require.NoError(t, err)
	recovered := workflow.NewEngine(restarted)

	require.NoError(t, recovered.AdvanceNode(ctx, "o-1", "vip"))
	require.NoError(t, recovered.AdvanceNode(ctx, "o-1", "done"))

	final, err := restarted.Get(ctx, "o-1")
	require.NoError(t, err)
	require.Equal(t, workflow.InstanceStatusCompleted, final.Status)
	require.Equal(t, []string{"review", "vip", "done"}, final.CompletedNodeIDs)
}

// TestSQLStore_EngineScanTimeouts 验证超时"发现"能力在 SQL 存储上端到端可用。
func TestSQLStore_EngineScanTimeouts(t *testing.T) {
	ctx := context.Background()
	store, _ := setupStore(t)
	engine := workflow.NewEngine(store)

	def := &workflow.Definition{
		ID:          "slow",
		StartNodeID: "wait",
		Nodes: []workflow.Node{
			{ID: "wait", Timeout: time.Nanosecond, Edges: []workflow.Edge{workflow.NewEdge("done")}},
			{ID: "done"},
		},
	}
	require.NoError(t, engine.SaveDefinition(ctx, def))
	require.NoError(t, engine.CreateInstance(ctx, "s-1", "slow"))
	require.NoError(t, engine.StartInstance(ctx, "s-1"))

	time.Sleep(2 * time.Millisecond)

	found, err := engine.ScanTimeouts(ctx, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, workflow.ID("s-1"), found[0].InstanceID)
	require.Equal(t, []string{"wait"}, found[0].NodeIDs)
}
