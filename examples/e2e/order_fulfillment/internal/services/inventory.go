package services

import (
	"context"
	"database/sql"
	"fmt"
	"gochen/db"
	"gochen/errors"
)

// InventoryService 库存领域服务
type InventoryService struct {
	database db.IDatabase
}

// NewInventoryService 创建库存服务
func NewInventoryService(database db.IDatabase) *InventoryService {
	return &InventoryService{database: database}
}

// Reserve 预留库存（正向操作）
func (s *InventoryService) Reserve(ctx context.Context, itemID string, quantity int) error {
	if quantity <= 0 {
		return errors.NewCode(errors.InvalidInput, "quantity must be positive")
	}

	// 查询可用库存
	row := s.database.QueryRow(ctx, "SELECT available FROM inventory WHERE item_id = ?", itemID)
	var available int
	if err := row.Scan(&available); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.NewCode(errors.NotFound, fmt.Sprintf("item %s not found in inventory", itemID))
		}
		return errors.Wrap(err, errors.Database, "query inventory failed")
	}

	if available < quantity {
		return errors.NewCode(errors.Conflict, fmt.Sprintf("insufficient inventory: available %d, required %d", available, quantity))
	}

	// 锁定扣减
	res, err := s.database.Exec(ctx,
		"UPDATE inventory SET available = available - ?, reserved = reserved + ? WHERE item_id = ? AND available >= ?",
		quantity, quantity, itemID, quantity,
	)
	if err != nil {
		return errors.Wrap(err, errors.Database, "reserve inventory failed")
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return errors.NewCode(errors.Concurrency, "concurrent inventory deduction conflict")
	}

	return nil
}

// Release 释放已预留库存（逆向补偿操作）
func (s *InventoryService) Release(ctx context.Context, itemID string, quantity int) error {
	if quantity <= 0 {
		return nil
	}

	_, err := s.database.Exec(ctx,
		"UPDATE inventory SET available = available + ?, reserved = reserved - ? WHERE item_id = ?",
		quantity, quantity, itemID,
	)
	if err != nil {
		return errors.Wrap(err, errors.Database, "release inventory compensation failed")
	}
	return nil
}

// Fulfill 正式出库结算（完成履约，清除已预留的标记）
func (s *InventoryService) Fulfill(ctx context.Context, itemID string, quantity int) error {
	if quantity <= 0 {
		return nil
	}

	_, err := s.database.Exec(ctx,
		"UPDATE inventory SET reserved = reserved - ? WHERE item_id = ?",
		quantity, itemID,
	)
	if err != nil {
		return errors.Wrap(err, errors.Database, "fulfill inventory failed")
	}
	return nil
}

// GetStock 查询当前库存状况
func (s *InventoryService) GetStock(ctx context.Context, itemID string) (available int, reserved int, err error) {
	row := s.database.QueryRow(ctx, "SELECT available, reserved FROM inventory WHERE item_id = ?", itemID)
	if err := row.Scan(&available, &reserved); err != nil {
		return 0, 0, err
	}
	return available, reserved, nil
}
