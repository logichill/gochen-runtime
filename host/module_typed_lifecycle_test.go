package host_test

import (
	"context"
	"errors"
	"testing"

	"gochen-runtime/host"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	modruntime "gochen-runtime/host/module/runtime"
	"gochen/messaging"
)

type typedLifecycleWorker struct {
	starts, stops int
	startErr      error
	stopErr       error
}

func (w *typedLifecycleWorker) Type() string                                     { return "job.ready" }
func (w *typedLifecycleWorker) Handle(context.Context, messaging.IMessage) error { return nil }
func (w *typedLifecycleWorker) Start(context.Context) error                      { w.starts++; return w.startErr }
func (w *typedLifecycleWorker) Stop(ctx context.Context) error {
	w.stops++
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.stopErr
}

type typedLifecycleBus struct {
	handler                 messaging.IMessageHandler
	subscriptions, releases int
}

func (b *typedLifecycleBus) Subscribe(_ context.Context, _ string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	b.handler = handler
	b.subscriptions++
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.releases++
		return nil
	}, nil
}
func typedLifecycleHost(t *testing.T, bus *typedLifecycleBus, worker *typedLifecycleWorker) *modruntime.Host {
	t.Helper()
	ctor := func() (module.IModule, error) {
		return host.Module("jobs").EventHandler(worker).RuntimeComponent(worker).Build()
	}
	h := modruntime.NewHost([]module.ModuleCtor{ctor}, config.WithEventBus(bus))
	if err := h.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTypedModuleMultiRoleReleasesTokensAndWorkerSeparately(t *testing.T) {
	worker := &typedLifecycleWorker{}
	bus := &typedLifecycleBus{}
	h := typedLifecycleHost(t, bus, worker)
	if err := h.StartBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.handler != worker || worker.starts != 1 || bus.subscriptions != 1 {
		t.Fatal("registered and started instances differ")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.releases != 1 || worker.stops != 1 {
		t.Fatalf("cleanup counts: subscription=%d worker=%d", bus.releases, worker.stops)
	}
}

func TestTypedModulePartialStartCleanupRetriesOnlyFailedRole(t *testing.T) {
	startErr := errors.New("partial worker start")
	stopErr := errors.New("worker stop pending")
	worker := &typedLifecycleWorker{startErr: startErr, stopErr: stopErr}
	bus := &typedLifecycleBus{}
	h := typedLifecycleHost(t, bus, worker)
	if err := h.StartBackground(context.Background()); !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("expected start and rollback errors, got %v", err)
	}
	if bus.releases != 1 || worker.stops != 1 {
		t.Fatal("partial startup did not release subscriptions and retain failed worker")
	}
	if err := h.StartBackground(context.Background()); err == nil {
		t.Fatal("restart accepted with pending cleanup")
	}
	if worker.starts != 1 {
		t.Fatal("worker started twice before cleanup")
	}
	worker.stopErr = nil
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.releases != 1 || worker.stops != 2 {
		t.Fatalf("cleanup retry counts: subscription=%d worker=%d", bus.releases, worker.stops)
	}
}

func TestTypedModuleCanceledShutdownRetainsCleanupResponsibility(t *testing.T) {
	worker := &typedLifecycleWorker{}
	bus := &typedLifecycleBus{}
	h := typedLifecycleHost(t, bus, worker)
	if err := h.StartBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled shutdown, got %v", err)
	}
	if err := h.StartBackground(context.Background()); err == nil {
		t.Fatal("restart accepted after canceled shutdown")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.releases != 1 || worker.stops != 2 {
		t.Fatalf("cleanup counts: subscription=%d worker=%d", bus.releases, worker.stops)
	}
}

type equalValueEventHandler struct{ name string }

func (h equalValueEventHandler) Type() string                                   { return h.name }
func (equalValueEventHandler) Handle(context.Context, messaging.IMessage) error { return nil }

func TestTypedModuleEqualValueHandlersKeepIndependentSubscriptions(t *testing.T) {
	bus := &typedLifecycleBus{}
	first := equalValueEventHandler{name: "job.ready"}
	second := equalValueEventHandler{name: "job.ready"}
	h := modruntime.NewHost([]module.ModuleCtor{func() (module.IModule, error) {
		return host.Module("equal-handlers").EventHandler(first, second).Build()
	}}, config.WithEventBus(bus))
	if err := h.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.StartBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.subscriptions != 2 {
		t.Fatalf("equal value handlers conflated: %d subscriptions", bus.subscriptions)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bus.releases != 2 {
		t.Fatalf("equal value handler tokens conflated: %d releases", bus.releases)
	}
}
