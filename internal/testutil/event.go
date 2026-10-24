package testutil

import (
	"fmt"
	"sync/atomic"

	"gochen/eventing"
)

var eventSequence atomic.Uint64

// NewEvent 创建带进程内唯一 ID 的测试事件。
func NewEvent[ID comparable](aggregateID ID, aggregateType, eventType string, version uint64, data any, schemaVersion ...int) *eventing.Event[ID] {
	// 零填充确保同一时间戳下按 ID 字符串排序仍与生成顺序一致。
	id := fmt.Sprintf("test-event-%012d", eventSequence.Add(1))
	return eventing.NewEventWithID(id, aggregateID, aggregateType, eventType, version, data, schemaVersion...)
}
