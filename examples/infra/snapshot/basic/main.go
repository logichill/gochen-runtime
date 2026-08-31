package main

import (
	"context"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	ssnap "gochen-runtime/eventing/store/snapshot"
	"gochen/app/eventsourced"
	"gochen/db"
	"gochen/domain"
	deventsourced "gochen/domain/eventsourced"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	csnap "gochen/eventing/store/snapshot"
	"gochen/eventing/upcast"
	"gochen/gen"
	"gochen/messaging"
)

// Counter 事件载荷
type ValueSet struct{ V int }

// EventType 返回事件类型标识。
func (e *ValueSet) EventType() string { return "ValueSet" }

// Counter 演示可配合快照恢复的事件溯源计数器。
type Counter struct {
	*deventsourced.EventSourcedAggregate[int64] `aggregate:"Counter"`
	Value                                       int
}

// NewCounter 创建一个新的计数器聚合。
func NewCounter(metadataRegistry *deventsourced.MetadataRegistry, id int64) *Counter {
	counter, err := deventsourced.New[Counter, int64](metadataRegistry, id)
	if err != nil {
		log.Fatal(err)
	}
	return counter
}

// ApplyValueSet 用事件里的值覆盖当前计数器状态。
func (c *Counter) ApplyValueSet(evt *ValueSet) {
	c.Value = evt.V
}

// main 演示快照创建、恢复和增量重放的基本流程。
func main() {
	log.SetPrefix("[snapshot_demo] ")
	ctx := context.Background()
	metadataRegistry := deventsourced.NewMetadataRegistry()
	mustMetadata(metadataRegistry.Register(&Counter{}, "Counter"))

	// 1) 初始化 SQLite + SQL EventStore + SQL SnapshotStore
	database := mustNewDB(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	mustEnsureSnapshotTable(ctx, database)
	eventStore := store.NewMemoryEventStore[int64]() // 示例中事件仍用内存存储，快照落 SQLite 即可
	snapStore := ssnap.NewSQLStore(database, "event_snapshots")
	snapCfg := csnap.DefaultConfig()
	snapCfg.Frequency = 3 // 每 3 个事件建议创建一次快照
	snapMgr := csnap.NewManager[int64](snapStore, snapCfg)
	reg := registry.NewRegistry()
	must(reg.Register("ValueSet", func() any { return &ValueSet{} }))
	upgraders := upcast.NewUpgraderRegistry()

	// 2) 构建 ES 仓储
	storeAdapter, err := eventsourced.NewDomainEventStore(eventsourced.DomainEventStoreOptions[*Counter, int64]{
		AggregateType:    "Counter",
		EventIDGenerator: gen.NewUUIDGenerator(),
		EventStore:       eventStore,
		SnapshotManager:  snapMgr,
		EventRegistry:    reg,
		UpgraderRegistry: upgraders,
	})
	must(err)

	repo, err := eventsourced.NewEventSourcedRepository(eventsourced.RepositoryOptions[*Counter, int64]{
		AggregateType:    "Counter",
		Sample:           &Counter{},
		Factory:          func(id int64) (*Counter, error) { return NewCounter(metadataRegistry, id), nil },
		Store:            storeAdapter,
		MetadataRegistry: metadataRegistry,
	})
	must(err)

	aggID := int64(1001)
	// 3) 第一次：从头重放，不使用快照
	start := time.Now()
	createAndSave(ctx, repo, metadataRegistry, aggID, 5) // 写入 5 个事件
	firstLoaded, err := repo.Get(ctx, aggID)
	must(err)
	firstDuration := time.Since(start)
	fmt.Printf("First load: value=%d version=%d duration=%s (no snapshot)\n", firstLoaded.Value, firstLoaded.GetVersion(), firstDuration)

	// 4) 为当前聚合创建快照（示例中直接调用 Manager）
	must(snapMgr.CreateSnapshot(ctx, aggID, "Counter", firstLoaded, firstLoaded.GetVersion()))

	// 5) 第二次：先从快照恢复，再重放增量事件
	// 模拟“应用重启”：新仓储实例 + 从快照恢复
	// 这里直接手动调用 LoadSnapshot 演示：实际可在仓储层集成
	start = time.Now()
	// 加 3 条新增事件（版本 6~8）
	createAndSave(ctx, repo, metadataRegistry, aggID, 3)

	// 从快照加载聚合状态
	loadedFromSnap := NewCounter(metadataRegistry, aggID)
	if snap, err := snapMgr.LoadSnapshot(ctx, aggID, loadedFromSnap); err == nil {
		loadedFromSnap.SetVersion(snap.Version)
		// 从快照版本之后继续重放
		events, err := eventStore.LoadEvents(ctx, "Counter", aggID, snap.Version)
		must(err)
		for i := range events {
			evt := events[i]
			if de, ok := messaging.PayloadAs[domain.IDomainEvent](evt.GetPayload()); ok {
				must(loadedFromSnap.ApplyEvent(de))
			}
		}
	} else {
		log.Printf("load snapshot failed, fallback to full replay: %v", err)
		// 回退方案：从头重放
		events, err := eventStore.LoadEvents(ctx, "Counter", aggID, 0)
		must(err)
		for i := range events {
			evt := events[i]
			if de, ok := messaging.PayloadAs[domain.IDomainEvent](evt.GetPayload()); ok {
				must(loadedFromSnap.ApplyEvent(de))
			}
		}
	}
	secondDuration := time.Since(start)
	fmt.Printf("Second load: value=%d version=%d duration=%s (with snapshot)\n", loadedFromSnap.Value, loadedFromSnap.GetVersion(), secondDuration)
}

// createAndSave 读取聚合后连续追加 n 个递增事件并保存。
func createAndSave(ctx context.Context, repo deventsourced.IEventSourcedRepository[*Counter, int64], metadataRegistry *deventsourced.MetadataRegistry, id int64, n int) {
	agg, err := repo.GetOrCreate(ctx, id)
	must(err)
	if agg == nil {
		agg = NewCounter(metadataRegistry, id)
	}
	for i := 0; i < n; i++ {
		v := agg.Value + 1
		must(agg.ApplyAndRecord(&ValueSet{V: v}))
	}
	must(repo.Save(ctx, agg))
}

// mustNewDB 创建示例数据库，失败时直接终止程序。
func mustNewDB(cfg db.DBConfig) db.IDatabase {
	db, err := stdsql.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	return db
}

func mustEnsureSnapshotTable(ctx context.Context, database db.IDatabase) {
	_, err := database.Exec(ctx, `CREATE TABLE IF NOT EXISTS event_snapshots (
		aggregate_type TEXT NOT NULL,
		aggregate_id INTEGER NOT NULL,
		version INTEGER NOT NULL,
		data BLOB NOT NULL,
		timestamp DATETIME NOT NULL,
		metadata TEXT,
		PRIMARY KEY (aggregate_type, aggregate_id)
	)`)
	must(err)
}

// must 在示例里用于快速失败，避免重复编写错误处理样板。
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mustMetadata(_ *deventsourced.Metadata, err error) {
	must(err)
}
