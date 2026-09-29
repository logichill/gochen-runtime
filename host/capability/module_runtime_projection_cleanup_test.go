package capability

import (
	"context"
	"testing"

	"gochen/errors"
	eventbus "gochen/eventing/bus"
	"gochen/eventing/projection"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/messaging"
)

func TestModuleRuntime_ProjectionCleanupRetriesUnsubscribeNotFound(t *testing.T) {
	ctx := context.Background()
	missingErr := errors.NewCode(errors.NotFound, "subscription stream disappeared")
	unavailableErr := errors.NewCode(errors.Dependency, "subscription connection unavailable")
	bus := &cleanupFailureEventBus{
		failures: map[string]error{"missing": missingErr, "unavailable": unavailableErr},
		attempts: make(map[string]int),
	}
	manager, err := projection.NewProjectionManager[int64](nil, bus, registry.NewRegistry(), upcast.NewUpgraderRegistry())
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewModuleRuntime("test", NewRuntime(nil, manager, nil))
	runtime.AddProjection(0, func() (any, error) {
		return &cleanupFailureProjection{testProjectionBase: newTestProjection1().testProjectionBase}, nil
	})
	if err := runtime.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}

	err = runtime.Stop(ctx, nil)
	if !errors.Is(err, missingErr) || !errors.Is(err, unavailableErr) {
		t.Fatalf("Stop error = %v, want both unsubscribe failures", err)
	}
	if _, err := manager.ProjectionStatus("p1"); err != nil {
		t.Fatalf("failed cleanup lost its registration: %v", err)
	}
	bus.failures = nil
	if err := runtime.Stop(ctx, nil); err != nil {
		t.Fatalf("retry Stop: %v", err)
	}
	for _, eventType := range []string{"missing", "unavailable"} {
		if got := bus.attempts[eventType]; got != 2 {
			t.Fatalf("unsubscribe %s attempts = %d, want 2", eventType, got)
		}
	}
	if _, err := manager.ProjectionStatus("p1"); !errors.Is(err, errors.NotFound) {
		t.Fatalf("registration survived successful cleanup: %v", err)
	}
	if err := runtime.Start(ctx, nil); err != nil {
		t.Fatalf("restart after cleanup: %v", err)
	}
	if err := runtime.Stop(ctx, nil); err != nil {
		t.Fatalf("final Stop: %v", err)
	}
}

type cleanupFailureEventBus struct {
	eventbus.IEventBus
	failures map[string]error
	attempts map[string]int
}

func (b *cleanupFailureEventBus) SubscribeEvent(_ context.Context, eventType string, _ eventbus.IEventHandler) (messaging.UnsubscribeFunc, error) {
	return func(context.Context) error {
		b.attempts[eventType]++
		return b.failures[eventType]
	}, nil
}

type cleanupFailureProjection struct{ testProjectionBase }

func (*cleanupFailureProjection) SupportedEventTypes() []string {
	return []string{"missing", "unavailable"}
}
