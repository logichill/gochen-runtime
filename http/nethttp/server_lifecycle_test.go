package nethttp

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	runtimehttp "gochen-runtime/http"
	"gochen/errors"
)

func TestHTTPServer_StopBeforeStartPreventsLaterListen(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	if err := srv.Start("127.0.0.1:0"); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Start error = %v, want http.ErrServerClosed", err)
	}
}

func TestHTTPServer_StopWhileStartHasNotPublishedServer(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})

	// Start claims the lifecycle before registering routes. Holding mu creates the
	// exact window where shutdown used to observe a nil server and return early.
	srv.mu.Lock()
	startDone := make(chan error, 1)
	go func() {
		startDone <- srv.Start("127.0.0.1:0")
	}()
	waitForServerLifecycle(t, srv, serverLifecycleStarting)

	if err := srv.Stop(context.Background()); err != nil {
		srv.mu.Unlock()
		t.Fatalf("Stop returned error: %v", err)
	}
	srv.mu.Unlock()

	select {
	case err := <-startDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not exit after Stop won the unpublished-server race")
	}
}

func TestHTTPServer_StartFailureCanRetry(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{TLSEnabled: true})
	for attempt := 1; attempt <= 2; attempt++ {
		if err := srv.Start("127.0.0.1:0"); err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start attempt %d error = %v, want retryable TLS configuration error", attempt, err)
		}
	}
}

func TestHTTPServer_ConcurrentStartStopClosesListener(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close reservation listener: %v", err)
	}

	startDone := make(chan error, 1)
	go func() { startDone <- srv.Start(addr) }()
	waitForServerLifecycle(t, srv, serverLifecycleServing)

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	select {
	case err := <-startDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Stop")
	}
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listener still owns %s after Stop: %v", addr, err)
	}
	_ = probe.Close()
}

func TestHTTPServer_ConcurrentSecondStartIsRejected(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	startDone := make(chan error, 1)
	go func() { startDone <- srv.Start("127.0.0.1:0") }()
	waitForServerLifecycle(t, srv, serverLifecycleServing)

	if err := srv.Start("127.0.0.1:0"); !errors.Is(err, errors.Conflict) {
		t.Fatalf("second Start error = %v, want Conflict", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	select {
	case err := <-startDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("first Start error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Start did not return after Stop")
	}
}

func TestHTTPServer_StopRejectsNilContext(t *testing.T) {
	srv := NewServer(&runtimehttp.WebConfig{})
	if err := srv.Stop(nil); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("Stop error = %v, want InvalidInput", err)
	}
}

func waitForServerLifecycle(t *testing.T, srv *Server, want serverLifecycleState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		srv.lifecycleMu.Lock()
		got := srv.lifecycle
		srv.lifecycleMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("server lifecycle did not reach %v", want)
}
