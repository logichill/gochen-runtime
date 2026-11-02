package schema

import (
	"context"
	"fmt"
	"gochen/db"
)

// EnsureEventAndOutboxTables 确保事件And发件箱Tables。
func EnsureEventAndOutboxTables(ctx context.Context, db db.IDatabase) error {
	// 事件存储表（event_store）
	eventDDL := `
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
	);`
	if _, err := db.Exec(ctx, eventDDL); err != nil {
		return fmt.Errorf("create event_store: %w", err)
	}

	// Outbox 表（event_outbox）
	outboxDDL := `
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
	CREATE INDEX IF NOT EXISTS idx_event_outbox_retry_claim ON event_outbox (status, next_retry_at, created_at, id);
	CREATE INDEX IF NOT EXISTS idx_event_outbox_lease_claim ON event_outbox (status, lease_until, created_at, id);
	CREATE INDEX IF NOT EXISTS idx_event_outbox_aggregate ON event_outbox (aggregate_id, aggregate_type);
	`
	if _, err := db.Exec(ctx, outboxDDL); err != nil {
		return fmt.Errorf("create event_outbox: %w", err)
	}
	// 监控采集器同时查询 DLQ，示例提供完整的采集表结构。
	dlqDDL := `
	CREATE TABLE IF NOT EXISTS event_outbox_dlq (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		original_entry_id INTEGER NOT NULL,
		aggregate_id INTEGER NOT NULL,
		aggregate_type TEXT NOT NULL,
		event_id TEXT NOT NULL UNIQUE,
		event_type TEXT NOT NULL,
		event_data TEXT NOT NULL,
		failure_reason TEXT NULL,
		retry_count INTEGER NOT NULL,
		moved_at DATETIME NOT NULL
	);`
	if _, err := db.Exec(ctx, dlqDDL); err != nil {
		return fmt.Errorf("create event_outbox_dlq: %w", err)
	}
	return nil
}
