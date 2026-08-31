package capability

import (
	"context"
	"testing"

	"gochen/messaging"
)

func TestTransportContractAcceptsMessagingTransport(t *testing.T) {
	transport := newMockTransport()
	var _ ITransport = transport
	var _ messaging.ITransport = transport
}

type subscribeOnlyTransport struct{}

func (t *subscribeOnlyTransport) Subscribe(context.Context, string, messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error { return nil }, nil
}

func (t *subscribeOnlyTransport) Start(context.Context) error { return nil }

func (t *subscribeOnlyTransport) Stop(context.Context) error { return nil }

func TestTransportContractAllowsLifecycleSubscriberOnly(t *testing.T) {
	var _ ITransport = (*subscribeOnlyTransport)(nil)
}
