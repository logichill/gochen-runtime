package middleware

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gochen-runtime/http/nethttp"
	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
)

type capabilityRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
	pushed   string
	readFrom bool
	conn     net.Conn
}

type blockingBody struct {
	release <-chan struct{}
	closed  chan struct{}
}

type componentCountingLogger struct {
	withFieldCalls int
}

type blockingResponseStateOverrideContext struct {
	*nethttp.Context
	overrideStarted chan struct{}
	allowOverride   <-chan struct{}
	startOnce       sync.Once
}

func (c *blockingResponseStateOverrideContext) OverrideResponseState(status int, bytesWritten int64) {
	c.startOnce.Do(func() {
		close(c.overrideStarted)
		<-c.allowOverride
	})
	c.Context.OverrideResponseState(status, bytesWritten)
}

func (l *componentCountingLogger) Debug(context.Context, string, ...logging.Field) {}
func (l *componentCountingLogger) Info(context.Context, string, ...logging.Field)  {}
func (l *componentCountingLogger) Warn(context.Context, string, ...logging.Field)  {}
func (l *componentCountingLogger) Error(context.Context, string, ...logging.Field) {}
func (l *componentCountingLogger) WithFields(...logging.Field) logging.ILogger     { return l }
func (l *componentCountingLogger) WithField(string, any) logging.ILogger {
	l.withFieldCalls++
	return l
}

func (b *blockingBody) Read(_ []byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	close(b.closed)
	return nil
}

func newCapabilityRecorder() *capabilityRecorder {
	serverConn, clientConn := net.Pipe()
	clientConn.Close()
	return &capabilityRecorder{ResponseRecorder: httptest.NewRecorder(), conn: serverConn}
}

func (w *capabilityRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func (w *capabilityRecorder) Push(target string, opts *http.PushOptions) error {
	_ = opts
	w.pushed = target
	return nil
}

func (w *capabilityRecorder) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom = true
	return w.Body.ReadFrom(r)
}

type stubContext struct {
	reqCtx   httpx.IRequestContext
	aborted  bool
	status   int
	storage  map[string]httpx.ContextValue
	path     string
	clientIP string
}

func (c *stubContext) Method() string { return "GET" }
func (c *stubContext) Path() string {
	if c.path != "" {
		return c.path
	}
	return "/"
}
func (c *stubContext) Header(string) string { return "" }
func (c *stubContext) Query(string) string  { return "" }
func (c *stubContext) Param(string) string  { return "" }
func (c *stubContext) QueryParams() url.Values {
	return url.Values{}
}
func (c *stubContext) Body() ([]byte, error)  { return nil, nil }
func (c *stubContext) Request() *http.Request { return nil }
func (c *stubContext) ClientIP() string {
	if c.clientIP != "" {
		return c.clientIP
	}
	return ""
}
func (c *stubContext) UserAgent() string              { return "" }
func (c *stubContext) BindJSON(any) error             { return nil }
func (c *stubContext) BindQuery(any) error            { return nil }
func (c *stubContext) SetStatus(code int)             { c.status = code }
func (c *stubContext) SetHeader(string, string)       {}
func (c *stubContext) JSON(int, httpx.JSONBody) error { return nil }
func (c *stubContext) String(int, string) error       { return nil }
func (c *stubContext) Data(int, string, []byte) error { return nil }

func (c *stubContext) Set(key string, value httpx.ContextValue) {
	if c.storage == nil {
		c.storage = make(map[string]httpx.ContextValue)
	}
	c.storage[key] = value
}

func (c *stubContext) Get(key string) (httpx.ContextValue, bool) {
	if c.storage == nil {
		return httpx.ContextValue{}, false
	}
	v, ok := c.storage[key]
	return v, ok
}

func (c *stubContext) Required(key string) (httpx.ContextValue, error) {
	v, ok := c.Get(key)
	if !ok {
		return httpx.ContextValue{}, errors.New("missing key")
	}
	return v, nil
}

func (c *stubContext) Abort()                   { c.aborted = true }
func (c *stubContext) AbortWithStatus(code int) { c.aborted = true; c.status = code }
func (c *stubContext) AbortWithStatusJSON(code int, _ httpx.JSONBody) {
	c.aborted = true
	c.status = code
}
func (c *stubContext) IsAborted() bool { return c.aborted }

func (c *stubContext) RequestContext() httpx.IRequestContext { return c.reqCtx }
func (c *stubContext) SetContext(ctx httpx.IRequestContext)  { c.reqCtx = ctx }

func TestTimeout_NilContext_ReturnsError(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	if err := mw(nil, func() error { return nil }); err == nil {
		t.Fatalf("expected error, got nil")
	} else if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got: %#v", err)
	}
}

func TestTimeout_NilRequestContext_ReturnsError(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	ctx := &stubContext{reqCtx: nil}
	if err := mw(ctx, func() error { return nil }); err == nil {
		t.Fatalf("expected error, got nil")
	} else if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got: %#v", err)
	}
}

func TestTimeoutWithLoggerBuildsComponentLoggerOnce(t *testing.T) {
	logger := &componentCountingLogger{}
	mw := TimeoutWithLogger(time.Second, logger)
	if logger.withFieldCalls != 1 {
		t.Fatalf("component logger constructions = %d, want 1", logger.withFieldCalls)
	}
	for i := 0; i < 2; i++ {
		reqCtx, err := httpx.NewRequestContext(context.Background())
		if err != nil {
			t.Fatalf("NewRequestContext returned error: %v", err)
		}
		if err := mw(&stubContext{reqCtx: reqCtx}, func() error { return nil }); err != nil {
			t.Fatalf("TimeoutWithLogger returned error: %v", err)
		}
	}
	if logger.withFieldCalls != 1 {
		t.Fatalf("component logger constructions after requests = %d, want 1", logger.withFieldCalls)
	}
}

func TestTimeout_ReturnsWhenHandlerIgnoresCancellation(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	reqCtx, err := httpx.NewRequestContext(context.Background())
	if err != nil {
		t.Fatalf("NewRequestContext returned error: %v", err)
	}
	ctx := &stubContext{reqCtx: reqCtx}

	blockCh := make(chan struct{})
	start := time.Now()
	gotErr := mw(ctx, func() error {
		<-blockCh
		return nil
	})
	elapsed := time.Since(start)
	close(blockCh)

	if gotErr == nil {
		t.Fatalf("expected timeout middleware to return timeout error")
	}
	if !errors.Is(gotErr, errors.Timeout) {
		t.Fatalf("expected timeout error, got: %#v", gotErr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected bounded timeout response, elapsed=%v", elapsed)
	}
}

func TestTimeout_CommitsBufferedResponseOnSuccess(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		ctx.SetHeader("X-Test", "committed")
		return ctx.String(http.StatusAccepted, "ok")
	})
	if err != nil {
		t.Fatalf("Timeout returned error: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := rec.Header().Get("X-Test"); got != "committed" {
		t.Fatalf("X-Test = %q, want committed", got)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestTimeout_CommitsHeaderOnlyResponseOnSuccess(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		ctx.SetHeader("X-Test", "committed")
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error: %v", err)
	}
	if got := rec.Header().Get("X-Test"); got != "committed" {
		t.Fatalf("X-Test = %q, want committed", got)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestTimeout_DiscardedBufferedResponseAllowsErrorFallback(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		if err := ctx.String(http.StatusAccepted, "partial"); err != nil {
			return err
		}
		return errors.NewCode(errors.Internal, "boom")
	})
	if !errors.Is(err, errors.Internal) {
		t.Fatalf("Timeout error = %v, want Internal", err)
	}
	if err := nethttp.WriteErrorResponse(ctx, err); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if body := rec.Body.String(); strings.Contains(body, "partial") ||
		strings.Contains(body, "boom") ||
		!strings.Contains(body, "INTERNAL_ERROR") ||
		!strings.Contains(body, "internal server error") {
		t.Fatalf("body = %q, want safe error body without partial response", body)
	}
	if got, want := ctx.BytesWritten(), int64(rec.Body.Len()); got != want {
		t.Fatalf("context bytes written = %d, want %d", got, want)
	}
}

func TestTimeout_PreservesStreamingFlush(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			t.Fatal("response writer not available")
		}
		flusher, ok := writer.(http.Flusher)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Flusher")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if _, err := writer.Write([]byte("data: one\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		if body := rec.Body.String(); body != "data: one\n\n" {
			t.Fatalf("flush did not reach response immediately, body=%q", body)
		}
		if _, err := writer.Write([]byte("data: two\n\n")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error: %v", err)
	}
	if body := rec.Body.String(); body != "data: one\n\ndata: two\n\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestTimeout_FlushMarksResponseWrittenBeforeHandlerError(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			return fmt.Errorf("response writer not available")
		}
		flusher, ok := writer.(http.Flusher)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Flusher")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if _, err := writer.Write([]byte("data: one\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return errors.NewCode(errors.Internal, "stream failed after flush")
	})
	if !errors.Is(err, errors.Internal) {
		t.Fatalf("Timeout error = %v, want Internal", err)
	}
	if err := nethttp.WriteErrorResponse(ctx, err); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if body := rec.Body.String(); body != "data: one\n\n" {
		t.Fatalf("body = %q, want flushed stream without appended error", body)
	}
}

func TestTimeout_DoesNotAppendTimeoutJSONAfterStreamingFlush(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	release := make(chan struct{})
	defer close(release)
	flushed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- mw(ctx, func() error {
			writer, ok := nethttp.ResponseWriterOf(ctx)
			if !ok {
				return fmt.Errorf("response writer not available")
			}
			flusher, ok := writer.(http.Flusher)
			if !ok {
				return fmt.Errorf("response writer does not implement http.Flusher")
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			if _, err := writer.Write([]byte("data: one\n\n")); err != nil {
				return err
			}
			flusher.Flush()
			close(flushed)
			<-release
			return nil
		})
	}()

	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("streaming response was not flushed")
	}

	err = <-done
	if err != nil {
		t.Fatalf("Timeout returned error after streaming response: %v", err)
	}
	if body := rec.Body.String(); body != "data: one\n\n" {
		t.Fatalf("unexpected body after timeout: %q", body)
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestTimeout_NotBlockedByHandlerReadingBody(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	release := make(chan struct{})
	body := &blockingBody{release: release, closed: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, "/", body)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	start := time.Now()
	err = mw(ctx, func() error {
		_, bodyErr := ctx.Body()
		return bodyErr
	})
	elapsed := time.Since(start)
	close(release)

	if err != nil {
		t.Fatalf("Timeout returned error after writing timeout response: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout was blocked by body read, elapsed=%v", elapsed)
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatalf("handler body read did not finish after release")
	}
}

func TestTimeout_ResponseDoesNotWaitForSupervisorStop(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	err = mw(ctx, func() error {
		<-release
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Timeout returned error after writing timeout response: %v", err)
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}
	if elapsed >= 80*time.Millisecond {
		t.Fatalf("timeout response waited for supervisor stop, elapsed=%v", elapsed)
	}
}

func TestTimeout_StuckHandlerDoesNotCreateSupervisorWaiter(t *testing.T) {
	baseline := countTaskSupervisorStopFrames()
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	release := make(chan struct{})
	handlerDone := make(chan struct{})
	defer func() {
		close(release)
		select {
		case <-handlerDone:
		case <-time.After(time.Second):
			t.Fatal("stuck handler did not finish after release")
		}
	}()

	err = mw(ctx, func() error {
		defer close(handlerDone)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error after writing timeout response: %v", err)
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := countTaskSupervisorStopFrames(); got > baseline {
			t.Fatalf("timeout created TaskSupervisor.Stop waiter frames: before=%d after=%d", baseline, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func countTaskSupervisorStopFrames() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "gochen/process/task.(*TaskSupervisor).Stop")
}

func TestTimeout_OverridesBufferedContextStatus(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	handlerDone := make(chan struct{})

	err = mw(ctx, func() error {
		defer close(handlerDone)
		if writeErr := ctx.String(http.StatusCreated, "buffered"); writeErr != nil {
			return writeErr
		}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error after writing timeout response: %v", err)
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}
	if got := ctx.StatusCode(); got != http.StatusRequestTimeout {
		t.Fatalf("context status = %d, want %d", got, http.StatusRequestTimeout)
	}
	if body := rec.Body.String(); strings.Contains(body, "buffered") {
		t.Fatalf("timeout response contains discarded handler body: %q", body)
	}
	if got, want := ctx.BytesWritten(), int64(rec.Body.Len()); got != want {
		t.Fatalf("context bytes written = %d, want %d", got, want)
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish after release")
	}
}

func TestTimeout_LateContextWriteCannotOverrideTimeoutState(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	baseCtx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	allowOverride := make(chan struct{})
	ctx := &blockingResponseStateOverrideContext{
		Context:         baseCtx,
		overrideStarted: make(chan struct{}),
		allowOverride:   allowOverride,
	}
	lateWriteDone := make(chan error, 1)
	middlewareDone := make(chan error, 1)
	go func() {
		middlewareDone <- mw(ctx, func() error {
			<-ctx.overrideStarted
			writeErr := ctx.String(http.StatusCreated, "late")
			lateWriteDone <- writeErr
			return writeErr
		})
	}()

	select {
	case writeErr := <-lateWriteDone:
		if !errors.Is(writeErr, errors.Timeout) {
			t.Fatalf("late context write error = %v, want Timeout", writeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("late context write did not run during response override")
	}
	close(allowOverride)

	select {
	case err := <-middlewareDone:
		if err != nil {
			t.Fatalf("Timeout returned error after writing timeout response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout middleware did not finish after response override")
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}
	if got := ctx.StatusCode(); got != http.StatusRequestTimeout {
		t.Fatalf("context status = %d, want %d", got, http.StatusRequestTimeout)
	}
	if body := rec.Body.String(); strings.Contains(body, "late") {
		t.Fatalf("timeout response contains late handler body: %q", body)
	}
	if got, want := ctx.BytesWritten(), int64(rec.Body.Len()); got != want {
		t.Fatalf("context bytes written = %d, want %d", got, want)
	}
}

func TestTimeout_StreamingFlushThenContextWriteReturnsConflict(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- mw(ctx, func() error {
			writer, ok := nethttp.ResponseWriterOf(ctx)
			if !ok {
				return fmt.Errorf("response writer not available")
			}
			flusher, ok := writer.(http.Flusher)
			if !ok {
				return fmt.Errorf("response writer does not implement http.Flusher")
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			if _, err := writer.Write([]byte("data: one\n\n")); err != nil {
				return err
			}
			flusher.Flush()
			return ctx.String(http.StatusOK, "late")
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errors.Conflict) {
			t.Fatalf("Timeout error = %v, want Conflict", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("ctx.String after streaming flush deadlocked")
	}
	if body := rec.Body.String(); body != "data: one\n\n" {
		t.Fatalf("body = %q, want flushed stream without late write", body)
	}
}

func TestTimeout_FlushCommitsHeadersBeforeBody(t *testing.T) {
	mw := Timeout(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			return fmt.Errorf("response writer not available")
		}
		flusher, ok := writer.(http.Flusher)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Flusher")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher.Flush()
		if rec.Code != http.StatusOK {
			t.Fatalf("flush status = %d, want %d", rec.Code, http.StatusOK)
		}
		if contentType := rec.Header().Get("Content-Type"); contentType != "text/event-stream" {
			t.Fatalf("content type = %q", contentType)
		}
		if _, err := writer.Write([]byte("data: one\n\n")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error: %v", err)
	}
	if body := rec.Body.String(); body != "data: one\n\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestTimeout_PreservesResponseWriterCapabilities(t *testing.T) {
	mw := Timeout(time.Second)
	rec := newCapabilityRecorder()
	defer rec.conn.Close()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			return fmt.Errorf("response writer not available")
		}
		if _, ok := writer.(http.Hijacker); !ok {
			return fmt.Errorf("response writer does not implement http.Hijacker")
		}
		pusher, ok := writer.(http.Pusher)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Pusher")
		}
		if err := pusher.Push("/asset.js", nil); err != nil {
			return err
		}
		readerFrom, ok := writer.(io.ReaderFrom)
		if !ok {
			return fmt.Errorf("response writer does not implement io.ReaderFrom")
		}
		if _, err := readerFrom.ReadFrom(bytes.NewBufferString("ok")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Timeout returned error: %v", err)
	}
	if rec.pushed != "/asset.js" {
		t.Fatalf("Push target = %q", rec.pushed)
	}
	if ctx.BytesWritten() != 2 {
		t.Fatalf("ReadFrom bytes = %d, want 2", ctx.BytesWritten())
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestTimeout_HijackMarksResponseWritten(t *testing.T) {
	mw := Timeout(time.Second)
	rec := newCapabilityRecorder()
	defer rec.conn.Close()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			return fmt.Errorf("response writer not available")
		}
		if _, err := writer.Write([]byte("buffered")); err != nil {
			return err
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			return fmt.Errorf("response writer does not implement http.Hijacker")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return err
		}
		_ = conn.Close()
		return errors.NewCode(errors.Internal, "after hijack")
	})
	if !errors.Is(err, errors.Internal) {
		t.Fatalf("Timeout error = %v, want Internal", err)
	}
	if !rec.hijacked {
		t.Fatalf("expected underlying writer to be hijacked")
	}
	if err := nethttp.WriteErrorResponse(ctx, err); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("body = %q, want no buffered body or fallback error after hijack", body)
	}
}

func TestTimeout_LateHandlerWriteDoesNotReachResponse(t *testing.T) {
	mw := Timeout(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	release := make(chan struct{})
	lateDone := make(chan struct{})
	err = mw(ctx, func() error {
		writer, ok := nethttp.ResponseWriterOf(ctx)
		if !ok {
			return ctx.String(http.StatusOK, "late")
		}
		<-release
		defer close(lateDone)
		_, writeErr := writer.Write([]byte("late"))
		return writeErr
	})
	if err != nil {
		t.Fatalf("Timeout returned error after writing timeout response: %v", err)
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}

	close(release)
	select {
	case <-lateDone:
	case <-time.After(time.Second):
		t.Fatalf("late handler did not finish")
	}
	if body := rec.Body.String(); strings.Contains(body, "late") || len(body) == 0 {
		t.Fatalf("unexpected response body after late write: %q", body)
	}
}

func TestBufferedResponseWriter_EnforcesLimit(t *testing.T) {
	w := newBufferedResponseWriter(nil, 4, nil)
	if n, err := w.Write([]byte("1234")); err != nil || n != 4 {
		t.Fatalf("first Write = (%d, %v), want (4, nil)", n, err)
	}

	n, err := w.Write([]byte("5"))
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("overflow Write error = %v, want PayloadTooLarge", err)
	}
	if n != 0 {
		t.Fatalf("overflow Write n = %d, want 0", n)
	}
	if got := w.body.String(); got != "1234" {
		t.Fatalf("buffer body = %q, want %q", got, "1234")
	}
}

func TestBufferedResponseWriter_CloseDiscardsLateWrites(t *testing.T) {
	w := newBufferedResponseWriter(nil, 4, nil)
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatalf("Write before close error = %v", err)
	}

	w.close()
	n, err := w.Write([]byte("late-large-body"))
	if !errors.Is(err, errors.Timeout) {
		t.Fatalf("late Write error = %v, want Timeout", err)
	}
	if n != 0 {
		t.Fatalf("late Write n = %d, want 0", n)
	}
	if got := w.body.Len(); got != 0 {
		t.Fatalf("closed buffer length = %d, want 0", got)
	}
}

func TestBufferedResponseWriter_CloseConcurrentWithWrites(t *testing.T) {
	w := newBufferedResponseWriter(nil, 0, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, err := w.Write([]byte("late"))
				if err != nil && !errors.Is(err, errors.Timeout) {
					t.Errorf("Write returned error: %v", err)
					return
				}
			}
		}()
	}

	close(start)
	w.close()
	wg.Wait()
}

type warnCapturingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnCapturingLogger) Debug(context.Context, string, ...logging.Field) {}
func (l *warnCapturingLogger) Info(context.Context, string, ...logging.Field)  {}
func (l *warnCapturingLogger) Warn(_ context.Context, msg string, _ ...logging.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}
func (l *warnCapturingLogger) Error(context.Context, string, ...logging.Field) {}
func (l *warnCapturingLogger) WithFields(...logging.Field) logging.ILogger     { return l }
func (l *warnCapturingLogger) WithField(string, any) logging.ILogger           { return l }

func TestTimeoutWithConfig_MaxConcurrencyRejectsExcess(t *testing.T) {
	mw := TimeoutWithConfig(TimeoutConfig{
		Timeout:        time.Second,
		MaxConcurrency: 1,
	})

	release := make(chan struct{})
	handlerStarted := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		reqCtx, err := httpx.NewRequestContext(context.Background())
		if err != nil {
			t.Errorf("NewRequestContext error: %v", err)
			return
		}
		ctx := &stubContext{reqCtx: reqCtx}
		firstDone <- mw(ctx, func() error {
			close(handlerStarted)
			<-release
			return nil
		})
	}()

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("first handler did not start")
	}

	reqCtx2, err := httpx.NewRequestContext(context.Background())
	if err != nil {
		t.Fatalf("NewRequestContext error: %v", err)
	}
	ctx2 := &stubContext{reqCtx: reqCtx2}
	err = mw(ctx2, func() error { return nil })
	if !errors.Is(err, errors.TooManyRequests) {
		t.Fatalf("expected TooManyRequests, got: %v", err)
	}

	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first request returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not complete after release")
	}
}

func TestTimeoutWithConfig_StuckHandlerLogsWarning(t *testing.T) {
	logger := &warnCapturingLogger{}
	mw := TimeoutWithConfig(TimeoutConfig{
		Timeout: 10 * time.Millisecond,
		Logger:  logger,
	})

	release := make(chan struct{})
	defer close(release)

	reqCtx, err := httpx.NewRequestContext(context.Background())
	if err != nil {
		t.Fatalf("NewRequestContext error: %v", err)
	}
	ctx := &stubContext{reqCtx: reqCtx}
	_ = mw(ctx, func() error {
		<-release
		return nil
	})

	logger.mu.Lock()
	warns := logger.warns
	logger.mu.Unlock()

	if len(warns) == 0 {
		t.Fatal("expected at least one Warn log about stuck handler")
	}
	found := false
	for _, msg := range warns {
		if strings.Contains(msg, "handler goroutine may still be running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Warn log about stuck handler, got: %v", warns)
	}
}
