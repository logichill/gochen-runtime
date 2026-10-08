package runtime

import (
	"context"
	"testing"

	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/bootstrap"
	"gochen-runtime/host/module"
	"gochen/errors"
)

func TestShutdownStopsOnlyOwnedTransport(t *testing.T) {
	startErr := errors.NewCode(errors.Conflict, "start failed")
	for _, tc := range []struct {
		name       string
		running    bool
		startErr   error
		observable bool
		wantStops  int
	}{
		{name: "started here", observable: true, wantStops: 1},
		{name: "already running with idempotent start", running: true, observable: true},
		{name: "already running with failed start", running: true, startErr: startErr, observable: true},
		{name: "failed before running", startErr: startErr, observable: true},
		{name: "unobservable successful start", wantStops: 1},
		{name: "unobservable failed start", startErr: startErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			transport := &testTransport{running: tc.running, startErr: tc.startErr}
			var owned capability.ITransport = transport
			if !tc.observable {
				owned = &struct{ capability.ITransport }{transport}
			}
			host := &Host{runtime: &bootstrap.Runtime{Transport: owned}}
			if err := host.StartBackground(ctx); !errors.Is(err, tc.startErr) {
				t.Fatalf("StartBackground error = %v, want %v", err, tc.startErr)
			}
			for range 2 {
				if err := host.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if stops := transport.stopCalls + transport.stopWithSnapshotCalls; stops != tc.wantStops {
				t.Fatalf("stop calls = %d, want %d", stops, tc.wantStops)
			}
			if tc.running && !transport.running {
				t.Fatal("shutdown stopped a borrowed transport")
			}
		})
	}
}

func TestFailedTransportRollbackRetainsOwnershipForShutdown(t *testing.T) {
	for _, withModule := range []bool{false, true} {
		t.Run(map[bool]string{false: "no modules", true: "with module"}[withModule], func(t *testing.T) {
			ctx := context.Background()
			startErr := errors.NewCode(errors.Dependency, "transport partially started")
			stopErr := errors.NewCode(errors.Timeout, "transport stop timed out")
			transport := &testTransport{startErr: startErr, runningOnStartError: true, stopErr: stopErr}
			host := &Host{runtime: &bootstrap.Runtime{Transport: transport}}
			if withModule {
				host.modules = []module.IModule{&testModule{id: "test", name: "test"}}
			}
			if err := host.StartBackground(ctx); !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
				t.Fatalf("expected start and rollback errors, got %v", err)
			}
			if err := host.StartBackground(ctx); !errors.Is(err, errors.Conflict) {
				t.Fatalf("restart before cleanup = %v, want Conflict", err)
			}
			if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
				t.Fatalf("reload must retain failed startup cleanup: %v", err)
			}
			transport.stopErr = nil
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if transport.running || transport.stopWithSnapshotCalls != 2 {
				t.Fatalf("cleanup retry: running=%v stop calls=%d", transport.running, transport.stopWithSnapshotCalls)
			}
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if transport.stopWithSnapshotCalls != 2 {
				t.Fatal("shutdown repeated successful transport cleanup")
			}
			transport.startErr = nil
			if err := host.StartBackground(ctx); err != nil {
				t.Fatalf("restart after cleanup failed: %v", err)
			}
			if err := host.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFailedTransportStartStopsModulesBeforeTransport(t *testing.T) {
	for _, failModuleStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "module cleanup succeeds", true: "module cleanup retries"}[failModuleStop], func(t *testing.T) {
			ctx := context.Background()
			startErr := errors.New("transport partially started")
			moduleStopErr := errors.New("module cleanup failed")
			transport := &testTransport{startErr: startErr, runningOnStartError: true}
			stopCalls := 0
			m := &testModule{id: "dependent", name: "dependent", startFn: func(context.Context) (module.ModuleStopFunc, error) {
				return func(context.Context) error {
					stopCalls++
					if !transport.running {
						t.Error("transport stopped before module cleanup")
					}
					if failModuleStop && stopCalls == 1 {
						return moduleStopErr
					}
					return nil
				}, nil
			}}
			host := &Host{runtime: &bootstrap.Runtime{Transport: transport}, modules: []module.IModule{m}}
			err := host.StartBackground(ctx)
			if !errors.Is(err, startErr) || errors.Is(err, moduleStopErr) != failModuleStop {
				t.Fatalf("unexpected startup error: %v", err)
			}
			if failModuleStop {
				if !transport.running || transport.stopWithSnapshotCalls != 0 || !host.transportOwned || len(host.moduleStops) != 1 {
					t.Fatal("failed module cleanup must retain the running transport and module stop")
				}
				if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
					t.Fatalf("reload must retain failed module cleanup: %v", err)
				}
				if err := host.StartBackground(ctx); !errors.Is(err, errors.Conflict) {
					t.Fatalf("restart before cleanup = %v, want Conflict", err)
				}
			} else if transport.running || transport.stopWithSnapshotCalls != 1 {
				t.Fatal("successful module rollback must be followed by transport cleanup")
			}
			for range 2 {
				if err := host.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			}
			wantModuleStops := 1
			if failModuleStop {
				wantModuleStops = 2
			}
			if stopCalls != wantModuleStops || transport.running || transport.stopWithSnapshotCalls != 1 || host.transportOwned || len(host.moduleStops) != 0 {
				t.Fatalf("cleanup did not complete exactly once: module stops=%d, transport stops=%d", stopCalls, transport.stopWithSnapshotCalls)
			}
		})
	}
}

// 回归 R2a-F01：拒绝重载后仍须真正停止已启动的资源。
func TestLoadConfigPreservesShutdownAfterOwnedTransportStart(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		reload    bool
		wantStops int
	}{
		{name: "control without LoadConfig", wantStops: 1},
		{name: "LoadConfig after StartBackground", reload: true, wantStops: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &testTransport{}
			host := NewHost(nil, config.WithTransport(transport))
			if err := host.Prepare(ctx); err != nil {
				t.Fatalf("Prepare failed: %v", err)
			}
			if err := host.StartBackground(ctx); err != nil {
				t.Fatalf("StartBackground failed: %v", err)
			}
			if !host.transportOwned {
				t.Fatal("expected host to own the transport started by StartBackground")
			}

			if tc.reload {
				originalRuntime := host.runtime
				if err := host.LoadConfig(); !errors.Is(err, errors.Conflict) {
					t.Fatalf("LoadConfig must reject initialized host: %v", err)
				}
				if host.runtime != originalRuntime || !host.transportOwned || !host.backgroundStarted.Load() {
					t.Fatalf("LoadConfig must preserve lifecycle state: runtime_set=%v owned=%v started=%v stops=%d",
						host.runtime != nil, host.transportOwned, host.backgroundStarted.Load(), len(host.moduleStops))
				}
			}

			if err := host.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown failed: %v", err)
			}
			if got := transport.stopCalls + transport.stopWithSnapshotCalls; got != tc.wantStops {
				t.Fatalf("transport stop calls = %d, want %d", got, tc.wantStops)
			}
		})
	}
}
