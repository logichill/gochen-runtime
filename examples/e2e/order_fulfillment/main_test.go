package main

import (
	"context"
	"testing"
	"time"

	"gochen-runtime/examples/e2e/order_fulfillment/internal/access"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/domain"
	"gochen-runtime/examples/e2e/order_fulfillment/internal/saga"
)

func TestE2EOrderFulfillment_HappyPath(t *testing.T) {
	ctx := operatorContext(t)
	sys, err := BootstrapSystem(ctx)
	if err != nil {
		t.Fatalf("BootstrapSystem failed: %v", err)
	}
	defer func() { _ = sys.Transport.Stop(context.Background()) }()

	// 1. 创建订单
	order := &domain.Order{
		CustomerID:  2001,
		ItemID:      "item-phone",
		Quantity:    2,
		TotalAmount: 10000,
		Status:      domain.OrderStatusCreated,
	}
	if err := sys.OrderApp.Create(ctx, order); err != nil {
		t.Fatalf("Create order failed: %v", err)
	}

	// 2. 执行 Saga
	orderSaga := saga.NewFulfillmentSaga(
		"test-saga-happy",
		order.ID,
		order.CustomerID,
		order.ItemID,
		order.Quantity,
		order.TotalAmount,
		sys.CommandExecutor,
	)
	if err := sys.SagaOrchestrator.Execute(ctx, orderSaga); err != nil {
		t.Fatalf("Saga execution failed: %v", err)
	}

	// 3. 验证订单状态为 COMPLETED
	latestOrder, err := sys.OrderApp.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get order failed: %v", err)
	}
	if latestOrder.Status != domain.OrderStatusCompleted {
		t.Fatalf("expected order status %s, got %s", domain.OrderStatusCompleted, latestOrder.Status)
	}

	// 4. 验证库存扣减（10 -> 8 可用，0 预留）
	avail, res, err := sys.InventorySvc.GetStock(ctx, "item-phone")
	if err != nil {
		t.Fatalf("GetStock failed: %v", err)
	}
	if avail != 8 || res != 0 {
		t.Fatalf("expected available=8, reserved=0, got avail=%d, res=%d", avail, res)
	}

	// 5. 验证支付状态为 PAID
	_, payStatus, err := sys.PaymentSvc.GetPaymentStatus(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetPaymentStatus failed: %v", err)
	}
	if payStatus != "PAID" {
		t.Fatalf("expected payment status PAID, got %s", payStatus)
	}

	// 6. 验证 Outbox 事件发布
	_ = sys.OutboxPublisher.PublishPending(ctx)
	time.Sleep(50 * time.Millisecond)

	sys.eventsMu.Lock()
	events := append([]string(nil), sys.ReceivedEvents...)
	sys.eventsMu.Unlock()

	foundCompleted := false
	for _, evt := range events {
		if evt == "OrderCompleted" {
			foundCompleted = true
			break
		}
	}
	if !foundCompleted {
		t.Fatalf("expected OrderCompleted event in EventBus, got %v", events)
	}
}

func TestE2EOrderFulfillment_CompensationOnPaymentFailure(t *testing.T) {
	ctx := operatorContext(t)
	sys, err := BootstrapSystem(ctx)
	if err != nil {
		t.Fatalf("BootstrapSystem failed: %v", err)
	}
	defer func() { _ = sys.Transport.Stop(context.Background()) }()

	// 1. 创建订单（金额 60000 触发风控拒付）
	order := &domain.Order{
		CustomerID:  2002,
		ItemID:      "item-phone",
		Quantity:    3,
		TotalAmount: 60000,
		Status:      domain.OrderStatusCreated,
	}
	if err := sys.OrderApp.Create(ctx, order); err != nil {
		t.Fatalf("Create order failed: %v", err)
	}

	// 2. 执行 Saga，预期在支付步骤失败并触发自动逆向补偿
	orderSaga := saga.NewFulfillmentSaga(
		"test-saga-comp",
		order.ID,
		order.CustomerID,
		order.ItemID,
		order.Quantity,
		order.TotalAmount,
		sys.CommandExecutor,
	)
	err = sys.SagaOrchestrator.Execute(ctx, orderSaga)
	if err == nil {
		t.Fatal("expected Saga to fail and compensate, but got nil error")
	}

	// 3. 验证订单状态被补偿标记为 CANCELLED
	latestOrder, err := sys.OrderApp.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get order failed: %v", err)
	}
	if latestOrder.Status != domain.OrderStatusCancelled {
		t.Fatalf("expected order status %s, got %s", domain.OrderStatusCancelled, latestOrder.Status)
	}

	// 4. 验证预留库存已被补偿释放（库存恢复为 10，无锁死泄漏）
	avail, res, err := sys.InventorySvc.GetStock(ctx, "item-phone")
	if err != nil {
		t.Fatalf("GetStock failed: %v", err)
	}
	if avail != 10 || res != 0 {
		t.Fatalf("expected available=10, reserved=0 after compensation, got avail=%d, res=%d", avail, res)
	}

	// 5. 验证 Outbox 取消事件发布
	_ = sys.OutboxPublisher.PublishPending(ctx)
	time.Sleep(50 * time.Millisecond)

	sys.eventsMu.Lock()
	events := append([]string(nil), sys.ReceivedEvents...)
	sys.eventsMu.Unlock()

	foundCancelled := false
	for _, evt := range events {
		if evt == "OrderCancelled" {
			foundCancelled = true
			break
		}
	}
	if !foundCancelled {
		t.Fatalf("expected OrderCancelled event in EventBus, got %v", events)
	}
}

// operatorContext 建立与 HTTP 入口一致的身份上下文：租户、主体、当前范围与权限。
func operatorContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := access.WithIdentity(context.Background(), "tenant-apple", "alice", "1001",
		"order:api:create,order:api:read,order:api:list,order:api:update")
	if err != nil {
		t.Fatalf("build operator context: %v", err)
	}
	return ctx
}
