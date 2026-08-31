package db

import (
	"context"
	"fmt"
	"gochen/db"
)

// EnsureSchema 初始化端到端示例所需的所有 SQLite 表结构与测试数据
func EnsureSchema(ctx context.Context, database db.IDatabase) error {
	ddl := `
	-- 1. 订单主表
	CREATE TABLE IF NOT EXISTS orders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		customer_id INTEGER NOT NULL,
		item_id TEXT NOT NULL,
		quantity INTEGER NOT NULL,
		total_amount INTEGER NOT NULL,
		status TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 0
	);

	-- 2. 库存表
	CREATE TABLE IF NOT EXISTS inventory (
		item_id TEXT PRIMARY KEY,
		available INTEGER NOT NULL,
		reserved INTEGER NOT NULL DEFAULT 0
	);

	-- 3. 支付记录表
	CREATE TABLE IF NOT EXISTS payments (
		payment_id TEXT PRIMARY KEY,
		order_id INTEGER NOT NULL,
		amount INTEGER NOT NULL,
		status TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- 4. 事件存储表
	CREATE TABLE IF NOT EXISTS event_store (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		aggregate_id INTEGER NOT NULL,
		aggregate_type TEXT NOT NULL,
		version INTEGER NOT NULL,
		schema_version INTEGER NOT NULL,
		timestamp DATETIME NOT NULL,
		payload TEXT NOT NULL,
		metadata TEXT NOT NULL,
		UNIQUE(aggregate_id, aggregate_type, version)
	);

	-- 5. Outbox 发件箱表
	CREATE TABLE IF NOT EXISTS event_outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		aggregate_id INTEGER NOT NULL,
		aggregate_type TEXT NOT NULL,
		event_id TEXT NOT NULL UNIQUE,
		event_type TEXT NOT NULL,
		event_data TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		claim_token TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		published_at DATETIME NULL,
		retry_count INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NULL,
		lease_until DATETIME NULL,
		next_retry_at DATETIME NULL
	);
	CREATE INDEX IF NOT EXISTS idx_event_outbox_pending_claim ON event_outbox (status, created_at, id);
	`

	if _, err := database.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("ensure tables failed: %w", err)
	}

	// 初始化库存数据：商品 "item-phone" 库存 10 件
	initInventory := `
	INSERT OR REPLACE INTO inventory (item_id, available, reserved) 
	VALUES ('item-phone', 10, 0);
	`
	if _, err := database.Exec(ctx, initInventory); err != nil {
		return fmt.Errorf("init inventory data failed: %w", err)
	}

	return nil
}
