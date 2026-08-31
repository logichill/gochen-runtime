package sqlstore

import (
	"context"
	"gochen-runtime/internal/testutil"
	"time"

	"gochen/db"
	"gochen/eventing"
	"gochen/messaging"
)

// MockEventStoreWithDB 模拟支持数据库接口的事件存储
type MockEventStoreWithDB struct {
	events []eventing.Event[int64]
}

// Init 注册数据。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *MockEventStoreWithDB) Init(ctx context.Context) error { return nil }

// AppendEvents 向事件存储追加事件。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - aggregateID：对象/实体标识
// - events：事件列表（待追加/发布）（类型：[]eventing.IStorableEvent[int64]）
// - expectedVersion：期望版本（用于乐观并发控制）（类型：uint64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *MockEventStoreWithDB) AppendEvents(ctx context.Context, aggregateType string, aggregateID int64, events []eventing.IStorableEvent[int64], expectedVersion uint64) error {
	m.append(events)
	return nil
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

// LoadEvents 解析数据。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - aggregateID：对象/实体标识
// - afterVersion：起始版本（用于增量读取/追赶）（类型：uint64）
//
// 返回：
// - result1：列表结果（元素类型：eventing.Event[int64]）
// - err：错误信息（nil 表示成功）
func (m *MockEventStoreWithDB) LoadEvents(ctx context.Context, aggregateType string, aggregateID int64, afterVersion uint64) ([]eventing.Event[int64], error) {
	var filtered []eventing.Event[int64]
	for _, event := range m.events {
		if event.GetAggregateType() == aggregateType &&
			event.GetAggregateID() == aggregateID &&
			event.GetVersion() > afterVersion {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

func (m *MockEventStoreWithDB) HasAggregate(ctx context.Context, aggregateType string, aggregateID int64) (bool, error) {
	version, err := m.GetAggregateVersion(ctx, aggregateType, aggregateID)
	return version > 0, err
}

func (m *MockEventStoreWithDB) GetAggregateVersion(_ context.Context, aggregateType string, aggregateID int64) (uint64, error) {
	var version uint64
	for _, event := range m.events {
		if event.GetAggregateType() == aggregateType && event.GetAggregateID() == aggregateID && event.GetVersion() > version {
			version = event.GetVersion()
		}
	}
	return version, nil
}

// StreamEvents 按游标遍历事件流。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - from：参数值（具体语义见函数上下文）（类型：time.Time）
//
// 返回：
// - result1：列表结果（元素类型：eventing.Event[int64]）
// - err：错误信息（nil 表示成功）
func (m *MockEventStoreWithDB) StreamEvents(ctx context.Context, from time.Time) ([]eventing.Event[int64], error) {
	return m.events, nil
}
