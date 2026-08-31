package testutil

import (
	"context"

	"gochen/messaging"
)

// MockTransport 用于测试的传输层 mock。
type MockTransport struct {
	Subscriptions map[string][]messaging.IMessageHandler
	StartCalls    int
	StopCalls     int
	Log           *[]string
}

// NewMockTransport 创建空 MockTransport。
func NewMockTransport() *MockTransport {
	return &MockTransport{
		Subscriptions: make(map[string][]messaging.IMessageHandler),
	}
}

// Publish 发布单条消息。
func (t *MockTransport) Publish(context.Context, messaging.IMessage) error { return nil }

// PublishAll 批量发布消息。
func (t *MockTransport) PublishAll(context.Context, []messaging.IMessage) error { return nil }

// Subscribe 订阅消息。
func (t *MockTransport) Subscribe(ctx context.Context, messageType string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	if t.Subscriptions == nil {
		t.Subscriptions = make(map[string][]messaging.IMessageHandler)
	}
	t.Subscriptions[messageType] = append(t.Subscriptions[messageType], handler)
	if t.Log != nil {
		*t.Log = append(*t.Log, "subscribe:"+messageType)
	}
	return func(context.Context) error {
		if t.Log != nil {
			*t.Log = append(*t.Log, "unsubscribe:"+messageType)
		}
		return nil
	}, nil
}

// Start 启动传输层。
func (t *MockTransport) Start(context.Context) error {
	t.StartCalls++
	return nil
}

// Stop 停止传输层。
func (t *MockTransport) Stop(context.Context) error {
	t.StopCalls++
	return nil
}

// Stats 返回传输层统计信息。
func (t *MockTransport) Stats() messaging.TransportStats {
	return messaging.TransportStats{}
}

var _ messaging.ITransport = (*MockTransport)(nil)
