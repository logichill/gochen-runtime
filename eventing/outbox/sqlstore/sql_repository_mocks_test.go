package sqlstore

import (
	"context"
	"gochen-runtime/internal/testutil"

	"gochen/db"
	"gochen/eventing"
	"gochen/messaging"
)

// MockEventStoreWithDB 模拟支持数据库接口的事件存储
type MockEventStoreWithDB struct {
	events []eventing.Event[int64]
}

// AppendEventsWithDB ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - database：参数值（具体语义见函数上下文）（类型：db.IDatabase）
// - aggregateID：对象/实体标识
// - events：事件列表（待追加/发布）（类型：[]eventing.IStorableEvent[int64]）
// - expectedVersion：期望版本（用于乐观并发控制）（类型：uint64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *MockEventStoreWithDB) AppendEventsWithDB(ctx context.Context, database db.IDatabase, aggregateType string, aggregateID int64, events []eventing.IStorableEvent[int64], expectedVersion uint64) error {
	m.append(events)
	return nil
}

// append events：事件列表（待追加/发布）（类型：[]eventing.IStorableEvent[int64]）。
//
// 参数：
func (m *MockEventStoreWithDB) append(events []eventing.IStorableEvent[int64]) {
	for _, evt := range events {
		if e, ok := evt.(*eventing.Event[int64]); ok {
			m.events = append(m.events, *e)
			continue
		}
		payload := messaging.PayloadValue(evt.GetPayload())
		cloned := testutil.NewEvent[int64](evt.GetAggregateID(), evt.GetAggregateType(), evt.GetType(), evt.GetVersion(), payload, evt.EventSchemaVersion())
		metadata := evt.GetMetadata()
		for k, v := range metadata.MapCopy() {
			cloned.GetMetadata().Set(k, v)
		}
		m.events = append(m.events, *cloned)
	}
}
