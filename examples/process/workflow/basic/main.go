// Package main 演示 process/workflow 内核的完整装配与推进链路。
//
// 覆盖：SQL 存储注入、多版本定义、条件路由与默认（else）分支、节点级钩子、
// 全局拦截器、挂起/恢复/终止生命周期、节点超时巡检，以及进程重启后的实例恢复。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/process/workflow/sqlstore"
	"gochen/db"
	"gochen/process/workflow"

	_ "modernc.org/sqlite"
)

func main() {
	log.SetPrefix("[workflow_demo] ")
	log.SetFlags(0)
	ctx := context.Background()

	// 1. 外层装配基础设施：Gochen 内核只认 workflow.IStore 抽象，
	//    具体用 SQLite 还是 MySQL 由组合根决定。
	database := openDatabase()
	defer func() { _ = database.Close() }()

	store, err := sqlstore.NewSQLStore(database, nil)
	must(err)
	must(store.CreateTables(ctx))

	engine := buildEngine(store)

	// 2. 保存流程定义：条件路由 + 默认分支 + 节点超时。
	def := &workflow.Definition{
		ID:          "expense_approval",
		Name:        "报销审批",
		StartNodeID: "submit",
		Nodes: []workflow.Node{
			{ID: "submit", Edges: []workflow.Edge{workflow.NewEdge("review")}},
			{
				ID:      "review",
				Timeout: 24 * time.Hour,
				Edges: []workflow.Edge{
					// 大额走总监审批，其余走默认（else）分支。
					workflow.NewConditionalEdge("director", "data.amount > 1000"),
					workflow.NewDefaultEdge("finance"),
				},
				RejectTo: []string{"submit"},
			},
			{ID: "director", Edges: []workflow.Edge{workflow.NewEdge("finance")}},
			{ID: "finance", Kind: workflow.NodeKindTask, Edges: []workflow.Edge{workflow.NewEdge("done")}},
			{ID: "done"},
		},
	}
	must(engine.SaveDefinition(ctx, def))
	log.Printf("定义已保存：%s v%d", def.ID, def.Version)

	runLargeAmount(ctx, engine, store)
	runSmallAmountWithDefaultEdge(ctx, engine, store)
	runRejectAndLifecycle(ctx, engine, store)
	runTimeoutScan(ctx, store)
	runRestartRecovery(ctx, database)
}

// buildEngine 装配引擎：注入全局拦截器与节点级钩子。
func buildEngine(store workflow.IStore) *workflow.Engine {
	engine := workflow.NewEngine(store)

	// 全局拦截器：洋葱圈包住整次状态迁移，适合审计与埋点。
	engine.Use(func(ctx context.Context, tctx *workflow.TransitionContext, next func(context.Context) error) error {
		if err := next(ctx); err != nil {
			log.Printf("  [拦截器] action=%s instance=%s 失败：%v", tctx.Action, tctx.InstanceID, err)
			return err
		}
		log.Printf("  [拦截器] action=%s instance=%s node=%s", tctx.Action, tctx.InstanceID, tctx.NodeID)
		return nil
	})

	// 节点级钩子（通配）：观察每个节点的进入与离开。
	engine.OnNodeEnter(workflow.NodeAnyID, func(_ context.Context, hctx *workflow.NodeHookContext) error {
		log.Printf("  [钩子] 进入节点 %s（来自 %s，触发动作 %s）", hctx.NodeID, orDash(hctx.FromNodeID), hctx.Action)
		return nil
	})
	engine.OnNodeExit(workflow.NodeAnyID, func(_ context.Context, hctx *workflow.NodeHookContext) error {
		log.Printf("  [钩子] 离开节点 %s（触发动作 %s）", hctx.NodeID, hctx.Action)
		return nil
	})

	// 节点专属钩子：只在进入 director 时做业务校验，返回错误会否决整次推进。
	engine.OnNodeEnter("director", func(_ context.Context, hctx *workflow.NodeHookContext) error {
		log.Printf("  [钩子] director 前置校验：金额=%v", hctx.Data["amount"])
		return nil
	})

	return engine
}

// runLargeAmount 大额报销：条件命中，走 director 分支。
func runLargeAmount(ctx context.Context, engine *workflow.Engine, store workflow.IStore) {
	log.Println("\n--- 场景 1：大额报销（条件命中 → director） ---")
	const id = workflow.ID("exp-large")

	must(engine.CreateInstanceWithData(ctx, id, "expense_approval", map[string]any{"amount": 5000}))
	must(engine.StartInstance(ctx, id))
	must(engine.AdvanceNode(ctx, id, "submit"))
	must(engine.AdvanceNode(ctx, id, "review"))

	printState(ctx, store, id)
}

// runSmallAmountWithDefaultEdge 小额报销：条件不命中，走默认（else）边。
//
// 这是修复"条件全不命中被静默判定为 completed"缺陷后的行为：
// 必须存在兜底出边，否则定义在保存阶段就会被拒绝。
func runSmallAmountWithDefaultEdge(ctx context.Context, engine *workflow.Engine, store workflow.IStore) {
	log.Println("\n--- 场景 2：小额报销（条件不命中 → 默认边 finance） ---")
	const id = workflow.ID("exp-small")

	must(engine.CreateInstanceWithData(ctx, id, "expense_approval", map[string]any{"amount": 200}))
	must(engine.StartInstance(ctx, id))
	must(engine.AdvanceNode(ctx, id, "submit"))
	must(engine.AdvanceNode(ctx, id, "review"))

	printState(ctx, store, id)
}

// runRejectAndLifecycle 驳回与挂起/恢复/终止。
func runRejectAndLifecycle(ctx context.Context, engine *workflow.Engine, store workflow.IStore) {
	log.Println("\n--- 场景 3：驳回 + 挂起/恢复/终止 ---")
	const id = workflow.ID("exp-reject")

	must(engine.CreateInstanceWithData(ctx, id, "expense_approval", map[string]any{"amount": 300}))
	must(engine.StartInstance(ctx, id))
	must(engine.AdvanceNode(ctx, id, "submit"))

	// 驳回回到 submit：目标必须是严格支配祖先且在 RejectTo 白名单内。
	must(engine.RejectNode(ctx, id, "review", "submit"))
	printState(ctx, store, id)

	must(engine.SuspendInstance(ctx, id, "等待补充发票"))
	printState(ctx, store, id)

	must(engine.ResumeInstance(ctx, id))
	must(engine.TerminateInstance(ctx, id, "员工撤回申请"))
	printState(ctx, store, id)
}

// runTimeoutScan 演示超时"发现"：无需事先知道实例 ID。
func runTimeoutScan(ctx context.Context, store *sqlstore.SQLStore) {
	log.Println("\n--- 场景 4：超时巡检（ScanTimeouts 主动发现） ---")

	// 独立定义一个极短超时的节点，便于示例立即观察到超时。
	engine := workflow.NewEngine(store)
	def := &workflow.Definition{
		ID:          "quick_timeout",
		StartNodeID: "wait",
		Nodes: []workflow.Node{
			{ID: "wait", Timeout: 10 * time.Millisecond, Edges: []workflow.Edge{workflow.NewEdge("done")}},
			{ID: "done"},
		},
	}
	must(engine.SaveDefinition(ctx, def))
	must(engine.CreateInstance(ctx, workflow.ID("timeout-1"), "quick_timeout"))
	must(engine.StartInstance(ctx, workflow.ID("timeout-1")))

	time.Sleep(30 * time.Millisecond)

	timedOut, err := engine.ScanTimeouts(ctx, 50)
	must(err)
	for _, item := range timedOut {
		log.Printf("  发现超时实例 %s，超时节点 %v", item.InstanceID, item.NodeIDs)
	}
	if len(timedOut) == 0 {
		log.Println("  未发现超时实例")
	}
}

// runRestartRecovery 模拟进程重启：只保留数据库，实例状态可继续推进。
func runRestartRecovery(ctx context.Context, database db.IDatabase) {
	log.Println("\n--- 场景 5：进程重启后的断点恢复 ---")

	store, err := sqlstore.NewSQLStore(database, nil)
	must(err)
	engine := workflow.NewEngine(store)

	// exp-small 在场景 2 停在 finance，重启后继续推进到终点。
	const id = workflow.ID("exp-small")
	st, err := store.Get(ctx, id)
	must(err)
	log.Printf("  重启后读回 %s：status=%s active=%v（绑定定义 v%d）",
		id, st.Status, st.ActiveNodeIDs, st.DefinitionVersion)

	must(engine.AdvanceNode(ctx, id, "finance"))
	must(engine.AdvanceNode(ctx, id, "done"))
	printState(ctx, store, id)
}

func openDatabase() db.IDatabase {
	// 用内存库让示例免留文件；生产环境换成 MySQL/Postgres 即可，内核代码不变。
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     "file:workflow_demo?mode=memory&cache=shared",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	must(err)
	return database
}

func printState(ctx context.Context, store workflow.IStore, id workflow.ID) {
	st, err := store.Get(ctx, id)
	must(err)
	fmt.Printf("  => %s status=%-10s active=%v completed=%v version=%d\n",
		id, st.Status, st.ActiveNodeIDs, st.CompletedNodeIDs, st.Version)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
