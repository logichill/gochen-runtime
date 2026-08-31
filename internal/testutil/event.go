package testutil

import (
	"fmt"
	"sync/atomic"

	"gochen/eventing"
)

var eventSequence atomic.Uint64

// NewEvent 创建带进程内唯一 ID 的测试事件。
func NewEvent[ID comparable](aggregateID ID, aggregateType, eventType string, version uint64, data any, schemaVersion ...int) *eventing.Event[ID] {
	id := fmt.Sprintf("test-event-%d", eventSequence.Add(1))
	return eventing.NewEventWithID(id, aggregateID, aggregateType, eventType, version, data, schemaVersion...)
}
