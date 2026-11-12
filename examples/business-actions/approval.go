package main

import (
	"context"

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
	execute func(context.Context, ApproveOrder) (Approval, error)
}

// Approve 执行审批；回调失败触发事务回滚，错误时不返回成功 DTO。
// 提交或底层故障的错误向上传递；提交结果不明时须核对状态，不能直接假定可重试。
func (a *OrderApprover) Approve(ctx context.Context, input ApproveOrder) (Approval, error) {
	return a.execute(ctx, input)
}

// IOrderApprovalStore 是审批实际需要的窄仓储能力，不包含任意查询或删除。
type IOrderApprovalStore interface {
	Get(context.Context, int64) (*Order, error)
	Update(context.Context, *Order) error
}

type approvalAccess struct {
	orders   IOrderApprovalStore
	withinTx func(context.Context, func(context.Context) error) error
}

// approveOrder 保持用例顺序可见：事务、加载、版本检查、领域转换、保存、DTO。
// 此处没有动作字符串、授权 grant 或通用 Hook，访问条件已由装配端绑定。
func approveOrder(ctx context.Context, input ApproveOrder, access approvalAccess) (Approval, error) {
	var result Approval
	if input.ID <= 0 {
		return result, errors.NewCode(errors.InvalidInput, "order ID must be positive")
	}
	err := access.withinTx(ctx, func(txCtx context.Context) error {
		order, err := access.orders.Get(txCtx, input.ID)
		if err != nil {
			return err
		}
		if order.Version != input.ExpectedVersion {
			return errors.NewCode(errors.Concurrency, "order version has changed")
		}
		if err := order.Approve(input.Reason); err != nil {
			return err
		}
		if err := access.orders.Update(txCtx, order); err != nil {
			return err
		}
		result = Approval{OrderID: order.ID, Version: order.Version, Status: order.Status}
		return nil
	})
	if err != nil {
		return Approval{}, err
	}
	return result, nil
}
