package services

import (
	"context"
	"database/sql"
	"fmt"
	"gochen/db"
	"gochen/errors"
	"gochen/gen/uuid"
)

// PaymentService 支付领域服务
type PaymentService struct {
	database db.IDatabase
}

// NewPaymentService 创建支付服务
func NewPaymentService(database db.IDatabase) *PaymentService {
	return &PaymentService{database: database}
}

// ProcessPayment 扣除订单支付款项（正向操作）
func (s *PaymentService) ProcessPayment(ctx context.Context, orderID int64, amount int64) (string, error) {
	if amount <= 0 {
		return "", errors.NewCode(errors.InvalidInput, "payment amount must be positive")
	}

	// 业务模拟：当单笔金额 >= 50000 时模拟银行风控拒付，用于演示支付失败触发 Saga 补偿
	if amount >= 50000 {
		return "", errors.NewCode(errors.Forbidden, fmt.Sprintf("payment rejected by risk control: amount %d exceeds limit", amount))
	}

	paymentID := "pay-" + uuid.NewString()[:8]
	_, err := s.database.Exec(ctx,
		"INSERT INTO payments (payment_id, order_id, amount, status) VALUES (?, ?, ?, 'PAID')",
		paymentID, orderID, amount,
	)
	if err != nil {
		return "", errors.Wrap(err, errors.Database, "record payment failed")
	}

	return paymentID, nil
}

// RefundPayment 退款（逆向补偿操作）
func (s *PaymentService) RefundPayment(ctx context.Context, orderID int64) error {
	_, err := s.database.Exec(ctx,
		"UPDATE payments SET status = 'REFUNDED' WHERE order_id = ? AND status = 'PAID'",
		orderID,
	)
	if err != nil {
		return errors.Wrap(err, errors.Database, "refund payment compensation failed")
	}
	return nil
}

// GetPaymentStatus 查询支付状态
func (s *PaymentService) GetPaymentStatus(ctx context.Context, orderID int64) (string, string, error) {
	row := s.database.QueryRow(ctx, "SELECT payment_id, status FROM payments WHERE order_id = ?", orderID)
	var paymentID, status string
	if err := row.Scan(&paymentID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errors.NewCode(errors.NotFound, "payment not found")
		}
		return "", "", err
	}
	return paymentID, status, nil
}
