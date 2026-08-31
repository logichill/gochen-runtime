package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/eventing/monitoring"
	outboxsql "gochen-runtime/eventing/outbox/sqlstore"
	storesql "gochen-runtime/eventing/store/sqlstore"
	"gochen-runtime/examples/infra/outbox/sql/internal/schema"
	"gochen/app/eventsourced"
	"gochen/db"
	deventsourced "gochen/domain/eventsourced"
	"gochen/eventing"
	"gochen/eventing/bus"
	"gochen/eventing/monitoring"
	"gochen/eventing/outbox"
	"gochen/eventing/outbox/outboxmon"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/gen"
	"gochen/messaging"
	"gochen/messaging/transport/memory"
	"gochen/observe/logging"
)

// 领域事件
type Opened struct{ Initial int }

// Deposited 表示入金事件。
type Deposited struct{ Amount int }

// EventType 返回开户事件的类型标识。
func (e *Opened) EventType() string { return "Opened" }

// EventType 返回入金事件的类型标识。
func (e *Deposited) EventType() string { return "Deposited" }

// Account 演示接入 SQL EventStore 与 SQL Outbox 的账户聚合。
type Account struct {
	*deventsourced.EventSourcedAggregate[int64] `aggregate:"Account"`
	Balance                                     int
}

// NewAccount 创建一个新的账户聚合。
func NewAccount(metadataRegistry *deventsourced.MetadataRegistry, id int64) *Account {
	account, err := deventsourced.New[Account, int64](metadataRegistry, id)
	if err != nil {
		log.Fatal(err)
	}
	return account
}

// ApplyOpened 用开户事件初始化账户余额。
func (a *Account) ApplyOpened(evt *Opened) {
	a.Balance = evt.Initial
}

// ApplyDeposited 把入金金额累加到账户余额。
func (a *Account) ApplyDeposited(evt *Deposited) {
	a.Balance += evt.Amount
}

// main 演示 SQL EventStore 与 Outbox 原子写入、发布和重放读取。
func main() {
	log.SetPrefix("[sql_outbox_demo] ")
	ctx := context.Background()
	metadataRegistry := deventsourced.NewMetadataRegistry()
	mustMetadata(metadataRegistry.Register(&Account{}, "Account"))

	// 1) 初始化 SQLite DB 与表结构
	db := mustNewDB(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	must(schema.EnsureEventAndOutboxTables(ctx, db))

	// 2) 构建 SQL EventStore 与 Outbox Repository
	es, err := storesql.NewSQLEventStore(db, "event_store", storesql.WithLogger(logging.ComponentLogger("sql_outbox_demo.event_store")))
	must(err)
	ob, err := outboxsql.NewSimpleSQLOutboxRepository(db, es, nil)
	must(err)

	// （可选）启动监控端点：
	// MONITORING_ADDR=:8080 后访问：
	// - /internal/monitoring/healthz
	// - /internal/monitoring/readyz
	// - /internal/monitoring/metrics
	// - /internal/monitoring/snapshot
	monitoringAddr := os.Getenv("MONITORING_ADDR")
	var monitoringRegistry *monitoring.Registry
	if monitoringAddr != "" {
		monitoringRegistry = mustConfigureMonitoring(db, ob)

		mux := http.NewServeMux()
		mux.Handle("/internal/monitoring/", http.StripPrefix("/internal/monitoring",
			monitoringhttp.NewHandlerWithRoutes(monitoringRegistry, monitoring.FullRouteSet())))
		go func() {
			log.Printf("monitoring listening on http://%s/internal/monitoring/", monitoringAddr)
			_ = http.ListenAndServe(monitoringAddr, mux)
		}()
	}

	// 2.5) 显式创建 event registry/upgraders（不依赖任何全局默认值）
	eventReg := registry.NewRegistry()
	must(eventReg.Register("Opened", func() any { return &Opened{} }))
	must(eventReg.Register("Deposited", func() any { return &Deposited{} }))
	upgraders := upcast.NewUpgraderRegistry()

	// 3) 基础 ES 仓储（含 Outbox 支持）
	storeAdapter, err := eventsourced.NewDomainEventStore(eventsourced.DomainEventStoreOptions[*Account, int64]{
		AggregateType:    "Account",
		EventIDGenerator: gen.NewUUIDGenerator(),
		EventStore:       es,
		OutboxRepo:       ob,
		EventRegistry:    eventReg,
		UpgraderRegistry: upgraders,
	})
	must(err)

	base, err := eventsourced.NewEventSourcedRepository(eventsourced.RepositoryOptions[*Account, int64]{
		AggregateType:    "Account",
		Sample:           &Account{},
		Factory:          func(id int64) (*Account, error) { return NewAccount(metadataRegistry, id), nil },
		Store:            storeAdapter,
		MetadataRegistry: metadataRegistry,
	})
	must(err)
	repo := base

	// 4) 产生并保存事件（原子写入事件与 Outbox）
	acc := NewAccount(metadataRegistry, 9001)
	must(acc.ApplyAndRecord(&Opened{Initial: 100}))
	must(acc.ApplyAndRecord(&Deposited{Amount: 50}))
	must(repo.Save(ctx, acc))

	// 5) 创建事件总线 + Publisher，一次性发布 Outbox 待处理记录
	transport := memory.NewMemoryTransport(1024, 2)
	must(transport.Start(ctx))
	defer func() { _ = transport.Stop(context.Background()) }()
	eventBus := bus.NewEventBus(messaging.NewMessageBus(transport))
	// 订阅打印发布的事件
	eventUnsub, err := eventBus.SubscribeEvent(ctx, "*", bus.EventHandlerFunc(func(ctx context.Context, evt eventing.IEvent) error {
		aggID := int64(0)
		if typed, ok := evt.(eventing.ITypedEvent[int64]); ok {
			aggID = typed.GetAggregateID()
		}
		log.Printf("published event: %s v%d -> agg=%d", evt.GetType(), evt.GetVersion(), aggID)
		return nil
	}))
	must(err)
	defer func() { _ = eventUnsub(context.Background()) }()
	cfg := outbox.DefaultOutboxConfig()
	cfg.BatchSize = 100
	publisher, err := outbox.NewPublisher(ob, eventBus, cfg, nil, eventReg, upgraders)
	must(err)
	if monitoringRegistry != nil {
		must(publisher.SetMetricsRecorder(monitoringRegistry.Metrics))
	}
	must(publisher.PublishPending(ctx))

	// 6) 检查 Outbox 待发布数量（应变为 0）
	pending, err := ob.ClaimPendingEntries(ctx, 10)
	must(err)
	fmt.Printf("outbox pending(after publish)=%d\n", len(pending))

	// 7) 重放读取聚合状态
	loaded, err := base.Get(ctx, 9001)
	must(err)
	fmt.Printf("Account[%d] Balance=%d Version=%d\n", loaded.GetID(), loaded.Balance, loaded.GetVersion())

	// 如果启动了监控端点，则保持进程运行，便于手动查看指标与健康状态。
	if monitoringAddr != "" {
		select {}
	}
}

func configureMonitoring(database db.IDatabase, repo outbox.IOutboxRepository[int64]) (*monitoring.Registry, error) {
	collector, err := outboxsql.NewMetricsCollectorForRepository(database, repo)
	if err != nil {
		return nil, err
	}
	provider, err := outboxmon.NewProvider(collector)
	if err != nil {
		return nil, err
	}
	return monitoring.NewRegistry(
		monitoring.WithOutboxMetricsProvider(
			provider,
		),
	)
}

func mustConfigureMonitoring(database db.IDatabase, repo outbox.IOutboxRepository[int64]) *monitoring.Registry {
	reg, err := configureMonitoring(database, repo)
	must(err)
	must(monitoring.SetDefaultRegistry(reg))
	return reg
}

// mustNewDB 创建示例数据库，失败时直接终止程序。
func mustNewDB(cfg db.DBConfig) db.IDatabase {
	db, err := stdsql.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	return db
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
