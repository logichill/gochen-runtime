package domain

import (
	"gochen/domain"
	"gochen/errors"
)

// OrderStatus 订单状态枚举
type OrderStatus string

const (
	OrderStatusCreated           OrderStatus = "CREATED"
	OrderStatusInventoryReserved OrderStatus = "INVENTORY_RESERVED"
	OrderStatusPaid              OrderStatus = "PAID"
	OrderStatusCompleted         OrderStatus = "COMPLETED"
	OrderStatusCancelled         OrderStatus = "CANCELLED"
)

// Order 订单聚合根实体
//
// tenant 是隔离空间，由仓储按请求上下文权威盖戳；department 是授权维度，
// 由授权器在创建时赋权值，普通更新持续保护该维度。
type Order struct {
	ID          int64       `json:"id" db:"id" entity:"id"`
	Tenant      string      `json:"tenant" db:"tenant_id" dim:"tenant"`
	Department  int64       `json:"department" db:"department_id" dim:"department"`
	CustomerID  int64       `json:"customer_id" db:"customer_id"`
	ItemID      string      `json:"item_id" db:"item_id"`
	Quantity    int         `json:"quantity" db:"quantity"`
	TotalAmount int64       `json:"total_amount" db:"total_amount"`
	Status      OrderStatus `json:"status" db:"status"`
	Version     uint64      `json:"version" db:"version"`
}

// 确保实现 domain.IEntity 与 domain.ISettableID
var _ domain.IEntity[int64] = (*Order)(nil)
var _ domain.ISettableID[int64] = (*Order)(nil)
var _ domain.ISettableVersion = (*Order)(nil)
var _ domain.IValidatable = (*Order)(nil)

func (o *Order) GetID() int64 {
	if o == nil {
		return 0
	}
	return o.ID
}

func (o *Order) SetID(id int64) {
	if o != nil {
		o.ID = id
	}
}

func (o *Order) GetVersion() uint64 {
	if o == nil {
		return 0
	}
	return o.Version
}

func (o *Order) SetVersion(version uint64) {
	if o != nil {
		o.Version = version
	}
}

func (o *Order) Validate() error {
	if o == nil {
		return errors.NewCode(errors.InvalidInput, "order cannot be nil")
	}
	if o.CustomerID <= 0 {
		return errors.NewCode(errors.InvalidInput, "customer_id must be positive")
	}
	if o.ItemID == "" {
		return errors.NewCode(errors.InvalidInput, "item_id cannot be empty")
	}
	if o.Quantity <= 0 {
		return errors.NewCode(errors.InvalidInput, "quantity must be positive")
	}
	if o.TotalAmount < 0 {
		return errors.NewCode(errors.InvalidInput, "total_amount cannot be negative")
	}
	return nil
}

// 领域事件定义

// OrderCreatedEvent 订单创建事件
type OrderCreatedEvent struct {
	OrderID     int64  `json:"order_id"`
	CustomerID  int64  `json:"customer_id"`
	ItemID      string `json:"item_id"`
	Quantity    int    `json:"quantity"`
	TotalAmount int64  `json:"total_amount"`
}

func (e *OrderCreatedEvent) EventType() string { return "OrderCreated" }

// OrderCompletedEvent 订单履约成功事件
type OrderCompletedEvent struct {
	OrderID int64 `json:"order_id"`
}

func (e *OrderCompletedEvent) EventType() string { return "OrderCompleted" }

// OrderCancelledEvent 订单取消/补偿完成事件
type OrderCancelledEvent struct {
	OrderID int64  `json:"order_id"`
	Reason  string `json:"reason"`
}

func (e *OrderCancelledEvent) EventType() string { return "OrderCancelled" }
