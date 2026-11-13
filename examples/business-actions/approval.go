package main

import (
	"context"

	"gochen/app/crud"
	"gochen/auth"
	"gochen/contextx"
	"gochen/errors"
)

// ApproveOrder 是调用方明确提交的审批意图，不接受部门或权限信息。
type ApproveOrder struct {
	ID              int64
	ExpectedVersion uint64
	Reason          string
}

// Approval 是审批结果，不把持久化实体暴露给调用方。
type Approval struct {
	OrderID int64  `json:"order_id"`
	Version uint64 `json:"version"`
	Status  string `json:"status"`
}

// IOrderApprover 是 HTTP 以外的业务调用入口。
type IOrderApprover interface {
	Approve(context.Context, ApproveOrder) (Approval, error)
}

// OrderApprover 将同一个受保护用例以业务名称提供给进程内调用方。
// ctx 必须带有可信入口建立的身份；直接调用不会绕过授权。
type OrderApprover struct {
	orders *crud.Application[*Order, int64]
}

// Approve 使用同一 CRUD 执行协议完成授权、事务、领域行为与条件写入。
func (a *OrderApprover) Approve(ctx context.Context, input ApproveOrder) (Approval, error) {
	if a == nil || a.orders == nil {
		return Approval{}, errors.NewCode(errors.InvalidInput, "order approver is not initialized")
	}
	if input.ID <= 0 {
		return Approval{}, errors.NewCode(errors.InvalidInput, "order ID must be positive")
	}
	order, err := a.orders.Execute(ctx, approveAction, input.ID, func(_ context.Context, order *Order) error {
		if order.Version != input.ExpectedVersion {
			return errors.NewCode(errors.Concurrency, "order version has changed")
		}
		return order.Approve(input.Reason)
	}, crud.WithReason(input.Reason))
	if err != nil && !contextx.IsAfterCommitError(err) {
		return Approval{}, err
	}
	return Approval{OrderID: order.ID, Version: order.Version, Status: order.Status}, err
}

// Execute 将具名审批方法适配到类型化 HTTP 入口，不增加第二条执行路径。
func (a *OrderApprover) Execute(ctx context.Context, input ApproveOrder) (Approval, error) {
	return a.Approve(ctx, input)
}

// AuthorizationBinding 从实际执行的应用及固定审批动作取得同源声明。
func (a *OrderApprover) AuthorizationBinding() (auth.EntryBinding, error) {
	if a == nil || a.orders == nil {
		return auth.EntryBinding{}, errors.NewCode(errors.InvalidInput, "order approver is not initialized")
	}
	return a.orders.AuthorizationBinding(approveAction)
}
