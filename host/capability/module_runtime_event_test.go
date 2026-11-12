package capability

import (
	"testing"

	"gochen/errors"
)

func TestEventEntryCollectionFreezesActualHandlerAndRequiresActivation(t *testing.T) {
	transport := newMockTransport()
	runtime := NewModuleRuntime("events", NewRuntime(nil, nil, transport))
	handler := newMultiTypeEventHandler()
	resolves := 0
	runtime.AddEventHandler(5, func() (any, error) { resolves++; return handler, nil })
	entries, err := runtime.RuntimeEventHandlers()
	if err != nil || len(entries) != 2 || resolves != 1 {
		t.Fatalf("actual entries=%+v resolves=%d err=%v", entries, resolves, err)
	}
	if err := runtime.Start(t.Context(), nil); !errors.Is(err, errors.FailedPrecondition) || len(transport.Subscriptions) != 0 {
		t.Fatalf("unvalidated subscriptions started: %v", err)
	}
	if err := runtime.ActivateEventHandlers(); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("unsealed entries activated: %v", err)
	}
	handler.msgTypes = []string{"changed.event"}
	entries[0].MessageType = "injected.event"
	if err := runtime.SealEventHandlers(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ActivateEventHandlers(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if resolves != 1 || len(transport.Subscriptions) != 2 || len(transport.Subscriptions["a.event"]) != 1 || len(transport.Subscriptions["b.event"]) != 1 {
		t.Fatalf("actual snapshot changed: resolves=%d subscriptions=%+v", resolves, transport.Subscriptions)
	}
	if err := runtime.Stop(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	runtime.AddEventHandler(6, func() (any, error) { return newTestEventHandler(), nil })
	if _, err := runtime.RuntimeEventHandlers(); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("late registration accepted by source: %v", err)
	}
	if err := runtime.Start(t.Context(), nil); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("late registration subscribed: %v", err)
	}
}

func TestEventEntryCollectionRejectsTypedNilHandler(t *testing.T) {
	runtime := NewModuleRuntime("events", NewRuntime(nil, nil, newMockTransport()))
	runtime.AddEventHandler(0, func() (any, error) { return (*testEventHandler)(nil), nil })
	if _, err := runtime.RuntimeEventHandlers(); !errors.Is(err, errors.Internal) {
		t.Fatalf("typed-nil handler accepted: %v", err)
	}
}
