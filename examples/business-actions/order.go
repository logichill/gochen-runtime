package main

import (
	"strings"

	"gochen/domain/crud"
	"gochen/errors"
)

// Order 保存订单当前状态；审批不需要软删除或审计实体家族。
type Order struct {
	crud.Entity[int64]
	DepartmentID int64  `json:"department_id" dim:"department"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
}

// Approve 只允许待审批订单通过，并要求非空理由；失败时不修改订单。
func (o *Order) Approve(reason string) error {
	if o.Status != "pending" {
		return errors.NewCode(errors.Conflict, "only pending orders can be approved")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.NewCode(errors.InvalidInput, "approval reason is required")
	}
	o.Status = "approved"
	o.Reason = reason
	return nil
}
