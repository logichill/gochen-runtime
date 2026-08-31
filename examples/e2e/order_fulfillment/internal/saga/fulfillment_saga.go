package saga

import (
	"context"
	"fmt"
	"gochen/messaging/command"
	"gochen/process/saga"
)

// 命令类型常量
const (
	CmdReserveInventory = "cmd.inventory.reserve"
	CmdReleaseInventory = "cmd.inventory.release"
	CmdProcessPayment   = "cmd.payment.process"
	CmdRefundPayment    = "cmd.payment.refund"
	CmdCompleteOrder    = "cmd.order.complete"
	CmdCancelOrder      = "cmd.order.cancel"
)

// InventoryPayload 库存命令载荷
type InventoryPayload struct {
	OrderID  int64  `json:"order_id"`
	ItemID   string `json:"item_id"`
	Quantity int    `json:"quantity"`
}

// PaymentPayload 支付命令载荷
type PaymentPayload struct {
	OrderID int64 `json:"order_id"`
	Amount  int64 `json:"amount"`
}

// OrderPayload 订单状态变更命令载荷
type OrderPayload struct {
	OrderID int64  `json:"order_id"`
	Reason  string `json:"reason,omitempty"`
}

// FulfillmentSaga 订单履约 Saga
type FulfillmentSaga struct {
	saga.BaseSaga
	sagaID      string
	orderID     int64
	customerID  int64
	itemID      string
	quantity    int
	amount      int64
	cmdExecutor command.ICommandExecutor
}

// NewFulfillmentSaga 创建履约 Saga 实例
func NewFulfillmentSaga(
	sagaID string,
	orderID int64,
	customerID int64,
	itemID string,
	quantity int,
	amount int64,
	cmdExecutor command.ICommandExecutor,
) *FulfillmentSaga {
	return &FulfillmentSaga{
		sagaID:      sagaID,
		orderID:     orderID,
		customerID:  customerID,
		itemID:      itemID,
		quantity:    quantity,
		amount:      amount,
		cmdExecutor: cmdExecutor,
	}
}

func (s *FulfillmentSaga) ID() string {
	return s.sagaID
}

// OnFailed 在 Saga 失败且补偿完成后触发，将订单标记为已取消并写入取消事件
func (s *FulfillmentSaga) OnFailed(ctx context.Context, err error) error {
	if s.cmdExecutor != nil {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-can-%s", s.sagaID),
			CmdCancelOrder,
			fmt.Sprintf("%d", s.orderID),
			"Order",
			&OrderPayload{
				OrderID: s.orderID,
				Reason:  fmt.Sprintf("Saga failed: %v", err),
			},
		)
		return s.cmdExecutor.Execute(ctx, cmd)
	}
	return nil
}

func (s *FulfillmentSaga) Steps() []*saga.SagaStep {
	orderIDStr := fmt.Sprintf("%d", s.orderID)

	// Step 1: 预留库存（失败补偿：释放库存）
	stepReserve := saga.NewSagaStep("ReserveInventory", func(ctx context.Context) (*command.Command, error) {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-res-%s", s.sagaID),
			CmdReserveInventory,
			orderIDStr,
			"Order",
			&InventoryPayload{
				OrderID:  s.orderID,
				ItemID:   s.itemID,
				Quantity: s.quantity,
			},
		)
		return cmd, nil
	}).WithCompensation(func(ctx context.Context) (*command.Command, error) {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-rel-%s", s.sagaID),
			CmdReleaseInventory,
			orderIDStr,
			"Order",
			&InventoryPayload{
				OrderID:  s.orderID,
				ItemID:   s.itemID,
				Quantity: s.quantity,
			},
		)
		return cmd, nil
	})

	// Step 2: 扣减支付（失败补偿：退款）
	stepPayment := saga.NewSagaStep("ProcessPayment", func(ctx context.Context) (*command.Command, error) {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-pay-%s", s.sagaID),
			CmdProcessPayment,
			orderIDStr,
			"Order",
			&PaymentPayload{
				OrderID: s.orderID,
				Amount:  s.amount,
			},
		)
		return cmd, nil
	}).WithCompensation(func(ctx context.Context) (*command.Command, error) {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-ref-%s", s.sagaID),
			CmdRefundPayment,
			orderIDStr,
			"Order",
			&PaymentPayload{
				OrderID: s.orderID,
				Amount:  s.amount,
			},
		)
		return cmd, nil
	})

	// Step 3: 完成订单状态变更与出库
	stepComplete := saga.NewSagaStep("CompleteOrder", func(ctx context.Context) (*command.Command, error) {
		cmd := command.NewCommand(
			fmt.Sprintf("cmd-cmp-%s", s.sagaID),
			CmdCompleteOrder,
			orderIDStr,
			"Order",
			&OrderPayload{
				OrderID: s.orderID,
			},
		)
		return cmd, nil
	})

	return []*saga.SagaStep{stepReserve, stepPayment, stepComplete}
}
