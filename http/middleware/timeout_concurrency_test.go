package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gochen-runtime/http/nethttp"
	"gochen/errors"
	"gochen/httpx"
)

func TestTimeout_LateReadFromDoesNotReachResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, err := nethttp.NewBaseContext(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		finished := make(chan error, 1)
		err = Timeout(time.Second)(ctx, func() error {
			src := io.MultiReader(&blockingBody{release: release}, strings.NewReader("late"))
			_, err := ctx.ResponseWriter().(io.ReaderFrom).ReadFrom(src)
			finished <- err
			return err
		})
		unblock()
		writeErr := <-finished
		if err != nil {
			t.Fatalf("streaming timeout returned an error response: %v", err)
		}
		if !errors.Is(writeErr, errors.Timeout) || rec.Body.Len() != 0 {
			t.Fatalf("ReadFrom wrote after timeout: err=%v body=%q", writeErr, rec.Body.String())
		}
	})
}

type timeoutFailingWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *timeoutFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTimeout_PropagatesBufferedWriteFailure(t *testing.T) {
	failure := errors.New("connection write failed")
	w := &timeoutFailingWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
	ctx, err := nethttp.NewBaseContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	err = Timeout(time.Second)(ctx, func() error { return ctx.String(http.StatusOK, "body") })
	if !errors.Is(err, failure) {
		t.Fatalf("buffered write failure lost: %v", err)
	}
}

func TestTimeout_PropagatesStreamingCommitFailure(t *testing.T) {
	for _, mode := range []string{"flush", "controller", "read_from"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("stream write failed")
			w := &timeoutFailingWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
			ctx, err := nethttp.NewBaseContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if err != nil {
				t.Fatal(err)
			}
			var observed error
			err = Timeout(time.Second)(ctx, func() error {
				writer := ctx.ResponseWriter()
				if _, err := writer.Write([]byte("buffered")); err != nil {
					return err
				}
				switch mode {
				case "flush":
					writer.(http.Flusher).Flush()
				case "controller":
					observed = http.NewResponseController(writer).Flush()
				case "read_from":
					_, observed = writer.(io.ReaderFrom).ReadFrom(strings.NewReader(""))
				}
				return observed
			})
			if !errors.Is(err, failure) {
				t.Fatalf("streaming commit error lost: %v", err)
			}
			if mode != "flush" && !errors.Is(observed, failure) {
				t.Fatalf("streaming operation did not return write error: %v", observed)
			}
		})
	}
}

type timeoutFlushFailingWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *timeoutFlushFailingWriter) FlushError() error { return w.err }

func TestTimeout_PropagatesFlushFailure(t *testing.T) {
	failure := errors.New("flush failed")
	w := &timeoutFlushFailingWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
	ctx, err := nethttp.NewBaseContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	err = Timeout(time.Second)(ctx, func() error {
		return http.NewResponseController(ctx.ResponseWriter()).Flush()
	})
	if !errors.Is(err, failure) {
		t.Fatalf("flush error lost: %v", err)
	}
}

func TestTimeout_PropagatesTimeoutResponseWriteFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("timeout response write failed")
		w := &timeoutFailingWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
		ctx, err := nethttp.NewBaseContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		defer close(release)
		err = Timeout(time.Second)(ctx, func() error { <-release; return nil })
		if !errors.Is(err, failure) {
			t.Fatalf("timeout response write error lost: %v", err)
		}
	})
}

type timeoutBlockingWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release <-chan struct{}
}

func (w *timeoutBlockingWriter) Write(data []byte) (int, error) {
	close(w.started)
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestTimeout_DoesNotReturnDuringPassthroughWrite(t *testing.T) {
	// sync.Mutex 的等待不是 synctest 的 durable block，此处使用显式取消和真实计时。
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	w := &timeoutBlockingWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: release}
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, err := nethttp.NewBaseContext(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(reqCtx))
	if err != nil {
		t.Fatal(err)
	}
	done, finished := make(chan error, 1), make(chan error, 1)
	go func() {
		done <- Timeout(time.Hour)(ctx, func() error {
			writer := ctx.ResponseWriter()
			writer.(http.Flusher).Flush()
			_, err := writer.Write([]byte("in flight"))
			finished <- err
			return err
		})
	}()
	<-w.started
	cancel()
	returnedEarly := false
	select {
	case err = <-done:
		returnedEarly = true
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if !returnedEarly {
		err = <-done
	}
	writeErr := <-finished
	if returnedEarly || err != nil || writeErr != nil {
		t.Fatalf("request lifetime ended before its write: early=%v middleware=%v write=%v", returnedEarly, err, writeErr)
	}
}

func TestTimeoutWithConfig_HoldsConcurrencyUntilHandlerExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mw := TimeoutWithConfig(TimeoutConfig{Timeout: time.Second, MaxConcurrency: 1})
		newContext := func() *stubContext {
			reqCtx, err := httpx.NewRequestContext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return &stubContext{reqCtx: reqCtx}
		}
		release := make(chan struct{})
		defer func() {
			if release != nil {
				close(release)
			}
		}()
		wait := release
		err := mw(newContext(), func() error {
			<-wait
			return nil
		})
		if !errors.Is(err, errors.Timeout) {
			t.Fatalf("first request = %v, want Timeout", err)
		}
		called := false
		err = mw(newContext(), func() error { called = true; return nil })
		if !errors.Is(err, errors.TooManyRequests) || called {
			t.Fatalf("request admitted while timed-out handler is active: err=%v called=%v", err, called)
		}

		close(release)
		release = nil
		synctest.Wait()
		if err := mw(newContext(), func() error { return nil }); err != nil {
			t.Fatalf("slot was not released after handler exit: %v", err)
		}
	})
}

func TestTimeoutWithConfig_ReleasesConcurrencyAfterPanic(t *testing.T) {
	mw := TimeoutWithConfig(TimeoutConfig{Timeout: time.Second, MaxConcurrency: 1})
	for i := 0; i < 2; i++ {
		reqCtx, err := httpx.NewRequestContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = mw(&stubContext{reqCtx: reqCtx}, func() error { panic("handler failed") })
		if !errors.Is(err, errors.Internal) {
			t.Fatalf("request %d = %v, want Internal", i, err)
		}
	}
}
