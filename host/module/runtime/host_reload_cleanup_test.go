package runtime

import (
	"context"
	"testing"

	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen/errors"
)

func TestLoadConfigRetainsOwnedCleanup(t *testing.T) {
	ctx := context.Background()
	transport := &testTransport{}
	moduleStops := 0
	m := &testModule{id: "quality", name: "quality", startFn: func(context.Context) (module.ModuleStopFunc, error) {
		return func(context.Context) error { moduleStops++; return nil }, nil
	}}
	host := NewHost([]module.ModuleCtor{func() (module.IModule, error) { return m, nil }}, config.WithTransport(transport))
	if err := host.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
		t.Fatalf("expected reload conflict: %v", err)
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("after shutdown: transport_running=%v transport_stops=%d module_stops=%d", transport.running, transport.stopCalls+transport.stopWithSnapshotCalls, moduleStops)
	if transport.running || moduleStops != 1 {
		t.Fatal("reload discarded ownership: started resources were not stopped")
	}
}

func TestLoadConfigLifecycleBoundaries(t *testing.T) {
	ctx := context.Background()
	host := NewHost(nil)
	for range 2 {
		if err := host.LoadConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	original := host.runtime
	for _, shutdown := range []bool{false, true} {
		if shutdown {
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
			t.Fatalf("reload after prepare: %v", err)
		}
		if host.runtime != original {
			t.Fatal("reload replaced runtime")
		}
	}
}

func TestLoadConfigRetainsFailedTransportCleanup(t *testing.T) {
	ctx := context.Background()
	transport := &testTransport{stopErr: errors.New("stop failed")}
	host := NewHost(nil, config.WithTransport(transport))
	if err := host.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.Shutdown(ctx); err == nil {
		t.Fatal("expected stop failure")
	}
	if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
		t.Fatalf("reload after cleanup failure: %v", err)
	}
	transport.stopErr = nil
	for range 2 {
		if err := host.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if transport.running || transport.stopWithSnapshotCalls != 2 || host.transportOwned {
		t.Fatal("cleanup retry lost or duplicated")
	}
}

func TestLoadConfigPreservesBorrowedTransport(t *testing.T) {
	ctx := context.Background()
	transport := &testTransport{running: true}
	host := NewHost(nil, config.WithTransport(transport))
	if err := host.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
		t.Fatalf("reload: %v", err)
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.running || transport.stopCalls+transport.stopWithSnapshotCalls != 0 {
		t.Fatal("borrowed transport was stopped")
	}
}

func TestShutdownReportsMissingOwnedRuntime(t *testing.T) {
	host := &Host{transportOwned: true}
	if err := host.Shutdown(context.Background()); !errors.Is(err, errors.Internal) {
		t.Fatalf("missing runtime: %v", err)
	}
	if !host.transportOwned {
		t.Fatal("ownership cleared without cleanup")
	}
}
