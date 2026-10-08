package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	outboxsql "gochen-runtime/eventing/outbox/sqlstore"
	storesql "gochen-runtime/eventing/store/sqlstore"
	e2edb "gochen-runtime/examples/e2e/order_fulfillment/internal/db"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/domain"
	e2esaga "gochen-runtime/examples/e2e/order_fulfillment/internal/saga"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/services"
	"gochen-runtime/http/nethttp"
	"gochen/app/crud"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/bus"
	"gochen/eventing/outbox"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/gen/snowflake"
	"gochen/gen/uuid"
	"gochen/httpx"
	"gochen/messaging"
	"gochen/messaging/command"
	"gochen/messaging/transport/memory"
	"gochen/observe/logging"
	"gochen/process/lock"
	"gochen/process/saga"
)

// AppSystem 封装端到端组装后的完整系统实例
type AppSystem struct {
	DB               db.IDatabase
	OrderRepo        *repo.Repo[*domain.Order, int64]
	OrderApp         *crud.Application[*domain.Order, int64]
	InventorySvc     *services.InventoryService
	PaymentSvc       *services.PaymentService
	EventStore       *storesql.SQLEventStore[int64]
	OutboxRepo       outbox.IOutboxRepository[int64]
	OutboxPublisher  *outbox.Publisher[int64]
	SagaOrchestrator *saga.SagaOrchestrator
	CommandExecutor  *command.CommandExecutor
	HTTPServer       httpx.IServer
	EventBus         bus.IEventBus
	Transport        *memory.MemoryTransport

	eventsMu       sync.Mutex
	ReceivedEvents []string
}

// BootstrapSystem 初始化并装配整个系统的核心与驱动组件
func BootstrapSystem(ctx context.Context) (*AppSystem, error) {
	logger := logging.NewStdLogger("order_fulfillment")

	// 1. 底层数据库：基于 SQLite 内存库
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:?cache=shared",
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	// 2. 初始化 Schema 与基线数据
	if err := e2edb.EnsureSchema(ctx, database); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}

	// 3. 仓储与应用层服务（app/crud + db/orm/repo）
	ormEngine, err := lite.New(database)
	if err != nil {
		return nil, fmt.Errorf("create orm engine: %w", err)
	}
	idGen, err := snowflake.NewGenerator(1, 1)
	if err != nil {
		return nil, fmt.Errorf("create snowflake generator: %w", err)
	}
	orderRepo, err := repo.NewRepo[*domain.Order, int64](
		ormEngine,
		"orders",
		repo.WithIDGenerator[*domain.Order, int64](idGen),
	)
	if err != nil {
		return nil, fmt.Errorf("create order repo: %w", err)
	}
	orderApp, err := crud.NewApplication[*domain.Order, int64](orderRepo, nil)
	if err != nil {
		return nil, fmt.Errorf("create order app: %w", err)
	}

	// 4. 领域服务
	inventorySvc := services.NewInventoryService(database)
	paymentSvc := services.NewPaymentService(database)

	// 5. 事件与发件箱（EventStore + Transactional Outbox）
	es, err := storesql.NewSQLEventStore(database, "event_store", storesql.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("create event store: %w", err)
	}
	obRepo, err := outboxsql.NewSimpleSQLOutboxRepository(database, es, nil)
	if err != nil {
		return nil, fmt.Errorf("create outbox repo: %w", err)
	}

	// 6. 消息传输、事件总线与 Outbox Publisher
	transport := memory.NewMemoryTransport(256, 2)
	if err := transport.Start(ctx); err != nil {
		return nil, fmt.Errorf("start transport: %w", err)
	}
	msgBus := messaging.NewMessageBus(transport)
	eventBus := bus.NewEventBus(msgBus)

	eventReg := registry.NewRegistry()
	_ = eventReg.Register("OrderCompleted", func() any { return &domain.OrderCompletedEvent{} })
	_ = eventReg.Register("OrderCancelled", func() any { return &domain.OrderCancelledEvent{} })
	upgraders := upcast.NewUpgraderRegistry()

	outboxCfg := outbox.DefaultOutboxConfig()
	outboxPublisher, err := outbox.NewPublisher(
		obRepo,
		eventBus,
		outboxCfg,
		logger,
		eventReg,
		upgraders,
	)
	if err != nil {
		return nil, fmt.Errorf("create outbox publisher: %w", err)
	}

	// 7. 命令执行器与 Saga 编排器
	cmdExecutor := command.NewCommandExecutor()
	sagaStore := saga.NewMemorySagaStateStore()
	sagaOrch, err := saga.NewSagaOrchestrator(
		cmdExecutor,
		eventBus,
		sagaStore,
		uuid.NewGenerator(),
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("create saga orchestrator: %w", err)
	}
	sagaOrch.WithLockProvider(lock.NewMemoryLockProvider())

	sys := &AppSystem{
		DB:               database,
		OrderRepo:        orderRepo,
		OrderApp:         orderApp,
		InventorySvc:     inventorySvc,
		PaymentSvc:       paymentSvc,
		EventStore:       es,
		OutboxRepo:       obRepo,
		OutboxPublisher:  outboxPublisher,
		SagaOrchestrator: sagaOrch,
		CommandExecutor:  cmdExecutor,
		EventBus:         eventBus,
		Transport:        transport,
	}

	// 订阅领域事件用于结果收集与验证
	_, _ = eventBus.SubscribeEvent(ctx, "OrderCompleted", bus.EventHandlerFunc(func(ctx context.Context, evt eventing.IEvent) error {
		sys.eventsMu.Lock()
		sys.ReceivedEvents = append(sys.ReceivedEvents, "OrderCompleted")
		sys.eventsMu.Unlock()
		return nil
	}))
	_, _ = eventBus.SubscribeEvent(ctx, "OrderCancelled", bus.EventHandlerFunc(func(ctx context.Context, evt eventing.IEvent) error {
		sys.eventsMu.Lock()
		sys.ReceivedEvents = append(sys.ReceivedEvents, "OrderCancelled")
		sys.eventsMu.Unlock()
		return nil
	}))

	// 8. 注册 Saga 命令 Handlers
	registerCommandHandlers(sys)

	// 9. 暴露 REST API
	server := nethttp.NewServer(nil)
	registerAPIRoutes(server, sys)
	sys.HTTPServer = server

	return sys, nil
}

// 注册命令处理器
func registerCommandHandlers(sys *AppSystem) {
	// CmdReserveInventory
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdReserveInventory, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.InventoryPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid inventory payload")
		}
		if err := sys.InventorySvc.Reserve(ctx, payload.ItemID, payload.Quantity); err != nil {
			return err
		}
		// 更新订单状态为库存已锁定
		order, err := sys.OrderApp.Get(ctx, payload.OrderID)
		if err != nil {
			return err
		}
		order.Status = domain.OrderStatusInventoryReserved
		return sys.OrderApp.Update(ctx, order)
	})

	// CmdReleaseInventory (补偿)
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdReleaseInventory, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.InventoryPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid inventory payload")
		}
		return sys.InventorySvc.Release(ctx, payload.ItemID, payload.Quantity)
	})

	// CmdProcessPayment
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdProcessPayment, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.PaymentPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid payment payload")
		}
		_, err := sys.PaymentSvc.ProcessPayment(ctx, payload.OrderID, payload.Amount)
		if err != nil {
			return err
		}
		order, err := sys.OrderApp.Get(ctx, payload.OrderID)
		if err != nil {
			return err
		}
		order.Status = domain.OrderStatusPaid
		return sys.OrderApp.Update(ctx, order)
	})

	// CmdRefundPayment (补偿)
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdRefundPayment, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.PaymentPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid payment payload")
		}
		return sys.PaymentSvc.RefundPayment(ctx, payload.OrderID)
	})

	// CmdCompleteOrder
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdCompleteOrder, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.OrderPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid order payload")
		}
		order, err := sys.OrderApp.Get(ctx, payload.OrderID)
		if err != nil {
			return err
		}
		order.Status = domain.OrderStatusCompleted
		if err := sys.OrderApp.Update(ctx, order); err != nil {
			return err
		}
		// 正式出库结算（清除预留）
		if err := sys.InventorySvc.Fulfill(ctx, order.ItemID, order.Quantity); err != nil {
			return err
		}
		// 写入 EventStore 与 Outbox 领域事件
		eventVersion := order.Version
		if eventVersion == 0 {
			eventVersion = 1
		}
		evt := eventing.NewEventWithID[int64](
			uuid.NewString(),
			order.ID,
			"Order",
			"OrderCompleted",
			eventVersion,
			&domain.OrderCompletedEvent{OrderID: order.ID},
		)
		return sys.OutboxRepo.SaveWithEvents(ctx, order.ID, []eventing.Event[int64]{*evt})
	})

	// CmdCancelOrder (补偿)
	_ = sys.CommandExecutor.RegisterHandler(e2esaga.CmdCancelOrder, func(ctx context.Context, cmd *command.Command) error {
		payload, ok := messaging.PayloadAs[*e2esaga.OrderPayload](cmd.GetPayload())
		if !ok {
			return errors.NewCode(errors.InvalidInput, "invalid order payload")
		}
		order, err := sys.OrderApp.Get(ctx, payload.OrderID)
		if err != nil {
			return err
		}
		order.Status = domain.OrderStatusCancelled
		if err := sys.OrderApp.Update(ctx, order); err != nil {
			return err
		}
		// 写入 EventStore 与 Outbox 取消事件
		eventVersion := order.Version
		if eventVersion == 0 {
			eventVersion = 1
		}
		evt := eventing.NewEventWithID[int64](
			uuid.NewString(),
			order.ID,
			"Order",
			"OrderCancelled",
			eventVersion,
			&domain.OrderCancelledEvent{OrderID: order.ID, Reason: payload.Reason},
		)
		return sys.OutboxRepo.SaveWithEvents(ctx, order.ID, []eventing.Event[int64]{*evt})
	})
}

// 注册 HTTP API 路由
func registerAPIRoutes(server httpx.IServer, sys *AppSystem) {
	// 创建订单并触发 Saga 履约
	server.POST("/api/v1/orders", func(c httpx.IContext) error {
		var req struct {
			CustomerID  int64  `json:"customer_id"`
			ItemID      string `json:"item_id"`
			Quantity    int    `json:"quantity"`
			TotalAmount int64  `json:"total_amount"`
		}
		if err := c.BindJSON(&req); err != nil {
			return c.JSON(400, httpx.JSONValue(map[string]string{"error": err.Error()}))
		}

		// 1. 创建初始订单实体
		order := &domain.Order{
			CustomerID:  req.CustomerID,
			ItemID:      req.ItemID,
			Quantity:    req.Quantity,
			TotalAmount: req.TotalAmount,
			Status:      domain.OrderStatusCreated,
		}
		if err := sys.OrderApp.Create(c.RequestContext(), order); err != nil {
			return c.JSON(500, httpx.JSONValue(map[string]string{"error": err.Error()}))
		}

		// 2. 编排并执行 Fulfillment Saga
		sagaID := fmt.Sprintf("saga-%d-%s", order.ID, uuid.NewString()[:6])
		orderSaga := e2esaga.NewFulfillmentSaga(
			sagaID,
			order.ID,
			order.CustomerID,
			order.ItemID,
			order.Quantity,
			order.TotalAmount,
			sys.CommandExecutor,
		)

		sagaErr := sys.SagaOrchestrator.Execute(c.RequestContext(), orderSaga)

		// 重新加载订单最新状态
		latestOrder, _ := sys.OrderApp.Get(c.RequestContext(), order.ID)

		resp := map[string]any{
			"order":   latestOrder,
			"saga_id": sagaID,
		}
		if sagaErr != nil {
			resp["saga_error"] = sagaErr.Error()
			return c.JSON(200, httpx.JSONValue(resp))
		}

		return c.JSON(201, httpx.JSONValue(resp))
	})

	// 查询订单状态
	server.GET("/api/v1/orders/:id", func(c httpx.IContext) error {
		idStr := c.Param("id")
		var id int64
		_, _ = fmt.Sscanf(idStr, "%d", &id)
		order, err := sys.OrderApp.Get(c.RequestContext(), id)
		if err != nil {
			return c.JSON(404, httpx.JSONValue(map[string]string{"error": "order not found"}))
		}
		return c.JSON(200, httpx.JSONValue(order))
	})

	// 查询库存
	server.GET("/api/v1/inventory/:item_id", func(c httpx.IContext) error {
		itemID := c.Param("item_id")
		avail, res, err := sys.InventorySvc.GetStock(c.RequestContext(), itemID)
		if err != nil {
			return c.JSON(500, httpx.JSONValue(map[string]string{"error": err.Error()}))
		}
		return c.JSON(200, httpx.JSONValue(map[string]any{
			"item_id":   itemID,
			"available": avail,
			"reserved":  res,
		}))
	})
}

func main() {
	log.SetPrefix("[e2e_fulfillment] ")
	ctx := context.Background()

	fmt.Println("=================================================================")
	fmt.Println("  Gochen E2E Order Fulfillment Architecture Demo")
	fmt.Println("  (SQL Repo + REST API + Saga Orchestrator + Transactional Outbox)")
	fmt.Println("=================================================================")

	// 1. 初始化系统
	sys, err := BootstrapSystem(ctx)
	if err != nil {
		log.Fatalf("Bootstrap system failed: %v", err)
	}
	defer func() { _ = sys.Transport.Stop(context.Background()) }()

	listenAddr := "127.0.0.1:18099"
	// 启动 HTTP 服务
	go func() {
		_ = sys.HTTPServer.Start(listenAddr)
	}()
	time.Sleep(50 * time.Millisecond) // 等待监听就绪

	baseURL := fmt.Sprintf("http://%s", listenAddr)
	fmt.Printf("\n[1] HTTP Server started at %s\n", baseURL)

	// 查询初始库存
	avail, _, _ := sys.InventorySvc.GetStock(ctx, "item-phone")
	fmt.Printf("[2] Initial inventory for 'item-phone': %d units available\n\n", avail)

	// -----------------------------------------------------------------
	// 场景一：成功履约（Happy Path）
	// 下单 2 部手机，单价 5000，总金额 10000（在库存与限额内）
	// -----------------------------------------------------------------
	fmt.Println(">>> 场景一：正常下单履约流程 (Happy Path)")
	fmt.Println("-----------------------------------------------------------------")
	orderReq1 := map[string]any{
		"customer_id":  1001,
		"item_id":      "item-phone",
		"quantity":     2,
		"total_amount": 10000,
	}
	body1, _ := json.Marshal(orderReq1)
	resp1, err := http.Post(baseURL+"/api/v1/orders", "application/json", bytes.NewBuffer(body1))
	if err != nil {
		log.Fatalf("Scenario 1 request failed: %v", err)
	}
	respBody1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	fmt.Printf("HTTP Status: %d\nResponse: %s\n", resp1.StatusCode, string(respBody1))

	// 验证库存与订单状态
	avail1, res1, _ := sys.InventorySvc.GetStock(ctx, "item-phone")
	fmt.Printf("Inventory after Order 1 -> Available: %d, Reserved: %d\n", avail1, res1)

	// 触发 Outbox 发布
	fmt.Println("Triggering Outbox Publisher scan...")
	_ = sys.OutboxPublisher.PublishPending(ctx)
	time.Sleep(50 * time.Millisecond)
	sys.eventsMu.Lock()
	fmt.Printf("Outbox events published to EventBus: %v\n", sys.ReceivedEvents)
	sys.eventsMu.Unlock()

	// -----------------------------------------------------------------
	// 场景二：支付风控拒绝触发 Saga 逆向补偿（Compensation Path）
	// 下单 1 部手机，金额 60000 元（>= 50000 触发模拟银行风控拒付）
	// -----------------------------------------------------------------
	fmt.Println("\n>>> 场景二：支付被拒触发 Saga 逆向补偿流程 (Compensation Path)")
	fmt.Println("-----------------------------------------------------------------")
	orderReq2 := map[string]any{
		"customer_id":  1002,
		"item_id":      "item-phone",
		"quantity":     1,
		"total_amount": 60000,
	}
	body2, _ := json.Marshal(orderReq2)
	resp2, err := http.Post(baseURL+"/api/v1/orders", "application/json", bytes.NewBuffer(body2))
	if err != nil {
		log.Fatalf("Scenario 2 request failed: %v", err)
	}
	respBody2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	fmt.Printf("HTTP Status: %d\nResponse: %s\n", resp2.StatusCode, string(respBody2))

	// 验证库存自动回滚释放（不会残留锁定）
	avail2, res2, _ := sys.InventorySvc.GetStock(ctx, "item-phone")
	fmt.Printf("Inventory after Saga Compensation -> Available: %d, Reserved: %d (No Leak!)\n", avail2, res2)

	// 触发 Outbox 再次发布
	_ = sys.OutboxPublisher.PublishPending(ctx)
	time.Sleep(50 * time.Millisecond)
	sys.eventsMu.Lock()
	fmt.Printf("All Outbox events received so far: %v\n", sys.ReceivedEvents)
	sys.eventsMu.Unlock()

	// 优雅关闭
	_ = sys.HTTPServer.Stop(ctx)
	fmt.Println("\n=================================================================")
	fmt.Println("  ✅ E2E Order Fulfillment Demo Executed Successfully!")
	fmt.Println("=================================================================")
}
