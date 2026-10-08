package sqlstore

// DefaultOutboxTable 是 SQL Outbox 默认表名。
const DefaultOutboxTable = "event_outbox"

// DefaultOutboxDLQTable 是 SQL 发布死信默认表名。
const DefaultOutboxDLQTable = "event_outbox_dlq"
