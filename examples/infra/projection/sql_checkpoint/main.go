package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/examples/infra/projection/sql_checkpoint/internal/checkpoint"
	"gochen-runtime/examples/infra/projection/sql_checkpoint/internal/proj"
	"gochen/db"
	"gochen/eventing"
	"gochen/eventing/bus"
	"gochen/eventing/projection"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen"
	"gochen/messaging"
	"gochen/messaging/transport/memory"
	_ "modernc.org/sqlite"
)

// main 演示投影检查点在“重启”前后的恢复效果。
func main() {
	log.SetPrefix("[projection_sql_checkpoint] ")
	ctx := context.Background()

	// SQLite 临时文件数据库（便于在同进程内模拟“重启后”持久化）
	tempDir, err := os.MkdirTemp("", "gochen-projection-sql-checkpoint-*")
	must(err)
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			log.Printf("remove temp dir: %v", err)
		}
	}()
	dbPath := filepath.Join(tempDir, "proj_cp_demo.db")
	db := mustNewDB(db.DBConfig{Driver: "sqlite", Database: dbPath})
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close db: %v", err)
		}
	}()

	// 事件总线（内存）
	transport := memory.NewMemoryTransport(1024, 2)
	must(transport.Start(ctx))
	defer func() {
		if err := transport.Stop(ctx); err != nil {
			log.Printf("stop transport: %v", err)
		}
	}()
	bus := bus.NewEventBus(messaging.NewMessageBus(transport))

	// 演示用事件存储（内存）；真实“进程重启后重放”需要持久化事件存储。
	eventStore := store.NewMemoryEventStore[int64]()
	reg := registry.NewRegistry()
	must(reg.RegisterWithVersion("OrderCreated", 1, func() any { return &proj.OrderCreated{} }))
	upgraders := upcast.NewUpgraderRegistry()
	eventIDGenerator := gen.NewUUIDGenerator()

	// 持久化检查点存储（SQLite示例实现）并确保表存在
	cp := checkpoint.NewSQLiteCheckpointStore(db, "projection_checkpoints")
	must(cp.EnsureTable(ctx))
	projectionConfig := projection.DefaultProjectionConfig()
	projectionConfig.CheckpointSaveInterval = 0
	projectionConfig.CheckpointSaveCount = 1

	// 注册投影并启用检查点
	pm, err := projection.NewProjectionManagerWithConfig[int64](eventStore, bus, reg, upgraders, projectionConfig)
	must(err)
	pm, err = pm.WithCheckpointStore(cp)
	must(err)
	p, err := proj.NewOrderAmountProjection()
	must(err)
	p, err = projection.NewCheckpointingProjector[int64](p, noopProjectionTxRunner{})
	must(err)
	must(pm.RegisterProjection(p))
	must(pm.StartProjection(p.Name()))

	// 发布事件
	var expectedVersion uint64
	for i := 1; i <= 3; i++ {
		evt, err := eventing.NewEvent[int64](eventIDGenerator, int64(1000), "Order", "OrderCreated", uint64(i), &proj.OrderCreated{Amount: i * 10})
		must(err)
		must(eventStore.AppendEvents(ctx, "Order", 1000, []eventing.IStorableEvent[int64]{evt}, expectedVersion))
		expectedVersion++
		must(bus.PublishEvent(ctx, evt))
	}
	time.Sleep(200 * time.Millisecond)
	st, _ := pm.ProjectionStatus(p.Name())
	fmt.Printf("Before restart: Processed=%d Total=%d\n", st.ProcessedEvents, proj.GetTotal())
	must(pm.StopProjection(p.Name()))

	// 模拟“重启”——重新创建管理器并从检查点恢复
	pm2, err := projection.NewProjectionManagerWithConfig[int64](eventStore, bus, reg, upgraders, projectionConfig)
	must(err)
	pm2, err = pm2.WithCheckpointStore(cp)
	must(err)
	// 需要重新注册投影实例
	p2, err := proj.NewOrderAmountProjection()
	must(err)
	p2, err = projection.NewCheckpointingProjector[int64](p2, noopProjectionTxRunner{})
	must(err)
	must(pm2.RegisterProjection(p2))
	must(pm2.ResumeFromCheckpoint(ctx, p2.Name()))
	for i := 4; i <= 5; i++ {
		evt, err := eventing.NewEvent[int64](eventIDGenerator, int64(1000), "Order", "OrderCreated", uint64(i), &proj.OrderCreated{Amount: i * 10})
		must(err)
		must(eventStore.AppendEvents(ctx, "Order", 1000, []eventing.IStorableEvent[int64]{evt}, expectedVersion))
		expectedVersion++
		must(bus.PublishEvent(ctx, evt))
	}
	time.Sleep(200 * time.Millisecond)
	st2, _ := pm2.ProjectionStatus(p2.Name())
	fmt.Printf("After restart: Processed=%d Total=%d\n", st2.ProcessedEvents, proj.GetTotal())
}

// must 在示例里用于快速失败，避免重复展开错误处理。
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

type noopProjectionTxRunner struct{}

func (noopProjectionTxRunner) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// mustNewDB 创建示例数据库，失败时直接终止程序。
func mustNewDB(cfg db.DBConfig) db.IDatabase {
	d, err := stdsql.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	return d
}
