package sqlstore

import (
	"context"

	"gochen/db"
	"gochen/eventing"
)

// IEventAppenderWithDB 将事件追加到调用方提供的数据库或事务作用域。
// 版本检查与写入必须原子执行；传入事务时不得提交调用方事务。
// Outbox 只消费此写入能力，不要求事件读取或全局扫描。
type IEventAppenderWithDB[ID comparable] interface {
	// AppendEventsWithDB 在指定数据库作用域内原子检查版本并追加事件。
	AppendEventsWithDB(ctx context.Context, database db.IDatabase, aggregateType string, aggregateID ID, events []eventing.IStorableEvent[ID], expectedVersion uint64) error
}
