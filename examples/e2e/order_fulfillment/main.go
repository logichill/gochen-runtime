package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	outboxsql "gochen-runtime/eventing/outbox/sqlstore"
	storesql "gochen-runtime/eventing/store/sqlstore"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/access"
	e2edb "gochen-runtime/examples/e2e/order_fulfillment/internal/db"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/domain"
	e2esaga "gochen-runtime/examples/e2e/order_fulfillment/internal/saga"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/services"
	"gochen-runtime/host/authz"
	"gochen-runtime/http/nethttp"
	"gochen/app"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/db"
	"gochen/domain/meta"
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

	// 事件流版本独立于实体乐观锁版本：前者按聚合事件序号递增。
	eventVersionsMu sync.Mutex
	eventVersions   map[int64]uint64
}

func (s *AppSystem) nextEventVersion(aggregateID int64) uint64 {
	s.eventVersionsMu.Lock()
	defer s.eventVersionsMu.Unlock()
	if s.eventVersions == nil {
		s.eventVersions = make(map[int64]uint64)
	}
	s.eventVersions[aggregateID]++
	return s.eventVersions[aggregateID]
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
		// 隔离键与维度能力在仓储构造期声明，用例层只消费既定契约。
		repo.WithIsolation[*domain.Order, int64](repo.IsolationCols{Column: "tenant_id"}),
		repo.WithResourceContract[*domain.Order, int64](app.ResourceContract{
			Isolation:  meta.ByDimension("tenant"),
			Assignable: []meta.Dimension{"department"},
		}),
		repo.WithResourceKind[*domain.Order, int64](access.Resource),
	)
	if err != nil {
		return nil, fmt.Errorf("create order repo: %w", err)
	}
	options := crud.Options[*domain.Order, int64]{Resource: access.Resource}
	descriptor, err := crud.Describe(options, true)
	if err != nil {
		return nil, fmt.Errorf("describe order application: %w", err)
	}
	// 动作权限、范围条件与创建赋值编译到同一份入口计划。
	options.Authorizer, err = auth.Compile(auth.Declaration{
		Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData,
		Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: access.DepartmentScope{}}}},
	}, auth.CompileOptions{Checker: authz.NewActionChecker(), Metadata: orderRepo.ExecutionDescriptor().Metadata()})
	if err != nil {
		return nil, fmt.Errorf("create order authorizer: %w", err)
	}
	orderApp, err := crud.NewApplication[*domain.Order, int64](orderRepo, options)
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
	// 独立示例显式开放私有演示账本，发布器仍执行固定注册函数的启用契约。
	if err := outboxPublisher.SetTaskExecutor(func(ctx context.Context, _ string, handler func(context.Context) error) error { return handler(ctx) }); err != nil {
		return nil, err
	}
	if err := outboxPublisher.TaskRegistry().SealHandlers(); err != nil {
		return nil, err
	}
	if err := outboxPublisher.TaskRegistry().ActivateHandlers(); err != nil {
		return nil, err
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
		eventVersion := sys.nextEventVersion(order.ID)
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
		eventVersion := sys.nextEventVersion(order.ID)
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
	// 认证入口：把租户、主体、当前范围与权限写入请求上下文，
	// 用例层据此判定动作与范围，仓储据此执行隔离。
	server.Use(access.BindIdentity())

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
			// 用例保留授权器的错误码，HTTP 层据此映射状态码（未认证 / 无权限 / 校验失败）。
			return err
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

// caller 描述一次调用已经通过认证入口的身份事实。
type caller struct {
	tenant      string
	operator    string
	scopeID     int64
	permissions string
}

// doJSON 以调用方身份发起 HTTP 请求：身份事实经请求头进入服务端。
//
// 本示例借此模拟可信认证入口；服务端不解析身份，只消费已经建立好的上下文。
func doJSON(ctx context.Context, method, url string, who caller, payload any) (int, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, "", err
		}
		body = bytes.NewBuffer(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, "", err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(access.TenantHeader, who.tenant)
	request.Header.Set(access.OperatorHeader, who.operator)
	request.Header.Set(access.ScopeHeader, strconv.FormatInt(who.scopeID, 10))
	request.Header.Set(access.PermissionHeader, who.permissions)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, string(raw), nil
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
	// 下单方：租户 tenant-apple 的部门 1001 操作员，持有下单与读取权限。
	ops := caller{tenant: "tenant-apple", operator: "alice", scopeID: 1001,
		permissions: "order:api:create,order:api:read,order:api:list,order:api:update"}
	orderReq1 := map[string]any{
		"customer_id":  1001,
		"item_id":      "item-phone",
		"quantity":     2,
		"total_amount": 10000,
	}
	status1, response1, err := doJSON(ctx, http.MethodPost, baseURL+"/api/v1/orders", ops, orderReq1)
	if err != nil {
		log.Fatalf("Scenario 1 request failed: %v", err)
	}
	fmt.Printf("HTTP Status: %d\nResponse: %s\n", status1, response1)

	var created struct {
		Order domain.Order `json:"order"`
	}
	if err := json.Unmarshal([]byte(response1), &created); err != nil {
		log.Fatalf("Scenario 1 response decode failed: %v", err)
	}
	fmt.Printf("订单归属由服务端落定: tenant=%s department=%d\n", created.Order.Tenant, created.Order.Department)

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
	status2, response2, err := doJSON(ctx, http.MethodPost, baseURL+"/api/v1/orders", ops, orderReq2)
	if err != nil {
		log.Fatalf("Scenario 2 request failed: %v", err)
	}
	fmt.Printf("HTTP Status: %d\nResponse: %s\n", status2, response2)

	// 验证库存自动回滚释放（不会残留锁定）
	avail2, res2, _ := sys.InventorySvc.GetStock(ctx, "item-phone")
	fmt.Printf("Inventory after Saga Compensation -> Available: %d, Reserved: %d (No Leak!)\n", avail2, res2)

	// 触发 Outbox 再次发布
	_ = sys.OutboxPublisher.PublishPending(ctx)
	time.Sleep(50 * time.Millisecond)
	sys.eventsMu.Lock()
	fmt.Printf("All Outbox events received so far: %v\n", sys.ReceivedEvents)
	sys.eventsMu.Unlock()

	// -----------------------------------------------------------------
	// 场景三：跨租户读取 —— 隔离空间不同，实体对其不可见
	// -----------------------------------------------------------------
	fmt.Println("\n>>> 场景三：另一租户读取订单 (L2 隔离)")
	fmt.Println("-----------------------------------------------------------------")
	intruder := caller{tenant: "tenant-google", operator: "mallory", scopeID: 1001,
		permissions: "order:api:read,order:api:list"}
	status3, response3, err := doJSON(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/orders/%d", baseURL, created.Order.ID), intruder, nil)
	if err != nil {
		log.Fatalf("Scenario 3 request failed: %v", err)
	}
	fmt.Printf("HTTP Status: %d（隔离范围内不可见按 404 处理，不泄露“存在但无权”）\nResponse: %s\n",
		status3, response3)

	// -----------------------------------------------------------------
	// 场景四：缺少动作权限 —— 在用例入口被拒绝
	// -----------------------------------------------------------------
	fmt.Println("\n>>> 场景四：只读操作员尝试下单 (L1 动作权限)")
	fmt.Println("-----------------------------------------------------------------")
	auditor := caller{tenant: "tenant-apple", operator: "bob", scopeID: 1001,
		permissions: "order:api:read,order:api:list"}
	status4, response4, err := doJSON(ctx, http.MethodPost, baseURL+"/api/v1/orders", auditor,
		map[string]any{"customer_id": 1003, "item_id": "item-phone", "quantity": 1, "total_amount": 5000})
	if err != nil {
		log.Fatalf("Scenario 4 request failed: %v", err)
	}
	fmt.Printf("HTTP Status: %d\nResponse: %s\n", status4, response4)

	// 优雅关闭
	_ = sys.HTTPServer.Stop(ctx)
	fmt.Println("\n=================================================================")
	fmt.Println("  ✅ E2E Order Fulfillment Demo Executed Successfully!")
	fmt.Println("=================================================================")
}
