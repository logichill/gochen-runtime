package middleware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"gochen/errors"
	"gochen/httpx"
	"gochen/observe/logging"
	"gochen/process/task"
)

type responseWriterProvider interface {
	ResponseWriter() http.ResponseWriter
	SwapResponseWriter(http.ResponseWriter) func()
	MarkResponseWritten()
	ResetResponseWritten()
}

type responseStateOverrider interface {
	OverrideResponseState(status int, bytesWritten int64)
}

const timeoutResponseBufferLimit = httpx.DefaultMaxBodySizeBytes

// bufferedResponseWriter 暂存 handler 正常完成前写出的响应。
//
// 约束：
// - 仅供 timeout middleware 内部使用；
// - handler 正常返回后才提交 buffer；超时后 close 与迟到 Write 可能并发；
// - 超时分支写回真实 writer 后不会再提交该 buffer，迟到写入会被丢弃。
type bufferedResponseWriter struct {
	mu          sync.Mutex
	header      http.Header
	body        bytes.Buffer
	limit       int64
	status      int
	wroteHeader bool
	closed      bool
	dst         http.ResponseWriter
	passthrough bool
	markWritten func()
}

func newBufferedResponseWriter(dst http.ResponseWriter, limit int64, markWritten func()) *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header), dst: dst, limit: limit, markWritten: markWritten}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	if w.passthrough {
		dst := w.dst
		markWritten := w.markWritten
		w.mu.Unlock()
		if dst != nil {
			if markWritten != nil {
				markWritten()
			}
			dst.WriteHeader(status)
		}
		return
	}

	w.writeHeaderLocked(status)
	w.mu.Unlock()
}

func (w *bufferedResponseWriter) writeHeaderLocked(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()
		return 0, errors.NewCode(errors.Timeout, "timeout response already closed")
	}
	if w.passthrough {
		dst := w.dst
		markWritten := w.markWritten
		w.mu.Unlock()
		if dst == nil {
			return len(data), nil
		}
		if markWritten != nil {
			markWritten()
		}
		return dst.Write(data)
	}
	if !w.wroteHeader {
		w.writeHeaderLocked(http.StatusOK)
	}
	if w.limit > 0 && int64(w.body.Len()+len(data)) > w.limit {
		w.mu.Unlock()
		return 0, errors.NewCode(errors.PayloadTooLarge, "timeout response buffer exceeded")
	}
	n, err := w.body.Write(data)
	w.mu.Unlock()
	return n, err
}

func (w *bufferedResponseWriter) commit(dst http.ResponseWriter) {
	if dst == nil {
		return
	}
	w.mu.Lock()
	if w.closed || w.passthrough {
		w.mu.Unlock()
		return
	}
	header := w.header.Clone()
	status := w.status
	wroteHeader := w.wroteHeader
	copyHeaders(dst.Header(), header)
	if wroteHeader {
		dst.WriteHeader(status)
	}
	if w.body.Len() > 0 {
		_, _ = w.body.WriteTo(dst)
	}
	w.mu.Unlock()
}

func (w *bufferedResponseWriter) Flush() {
	if w == nil {
		return
	}
	w.flushToPassthrough()
}

func (w *bufferedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w == nil {
		return nil, nil, http.ErrNotSupported
	}
	dst := w.startPassthrough()
	hijacker, ok := dst.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil && w.markWritten != nil {
		w.markWritten()
	}
	return conn, rw, err
}

func (w *bufferedResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.dstWriter()
}

func (w *bufferedResponseWriter) Push(target string, opts *http.PushOptions) error {
	if w == nil {
		return http.ErrNotSupported
	}
	dst := w.dstWriter()
	pusher, ok := dst.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *bufferedResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if w == nil {
		return 0, http.ErrNotSupported
	}
	dst := w.flushToPassthrough()
	if readerFrom, ok := dst.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(w, src)
}

func (w *bufferedResponseWriter) IsPassthrough() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.passthrough
}

func (w *bufferedResponseWriter) dstWriter() http.ResponseWriter {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst
}

func (w *bufferedResponseWriter) startPassthrough() http.ResponseWriter {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.body.Reset()
	w.passthrough = true
	return w.dst
}

func (w *bufferedResponseWriter) flushToPassthrough() http.ResponseWriter {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	dst := w.dst
	header := w.header.Clone()
	status := w.status
	wroteHeader := w.wroteHeader
	body := append([]byte(nil), w.body.Bytes()...)
	w.body.Reset()
	w.passthrough = true
	markWritten := w.markWritten
	w.mu.Unlock()

	if dst == nil {
		return nil
	}
	copyHeaders(dst.Header(), header)
	if markWritten != nil {
		markWritten()
	}
	if wroteHeader {
		dst.WriteHeader(status)
	} else {
		dst.WriteHeader(http.StatusOK)
	}
	if len(body) > 0 {
		_, _ = dst.Write(body)
	}
	if flusher, ok := dst.(http.Flusher); ok {
		flusher.Flush()
	}
	return dst
}

func (w *bufferedResponseWriter) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closed = true
	w.body.Reset()
}

func (w *bufferedResponseWriter) beginTimeoutResponse() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	if w.passthrough {
		w.closed = true
		w.body.Reset()
		return false
	}
	w.closed = true
	w.body.Reset()
	return true
}

func (w *bufferedResponseWriter) discardBuffered() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.passthrough {
		return false
	}
	w.closed = true
	w.body.Reset()
	return true
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// TimeoutConfig configures the timeout middleware.
type TimeoutConfig struct {
	// Timeout is the maximum duration for a handler to complete.
	// Defaults to 30s when <= 0.
	Timeout time.Duration

	// MaxConcurrency limits concurrent handler invocations across all requests
	// using the same middleware instance. 0 means unlimited. When > 0, excess
	// requests are rejected with errors.TooManyRequests (HTTP 429).
	MaxConcurrency int

	// Logger is the base logger for timeout diagnostics. nil uses a noop logger.
	Logger logging.ILogger
}

// Timeout 为整个 handler chain 提供超时控制。
//
// 说明：
//   - 该中间件通过 ctx cancellation 传递超时信号；无法强制终止不响应 ctx 的业务逻辑；
//   - 不启动常驻 goroutine，超时路径也不会再增加 Stop waiter；但忽略 ctx 的 handler
//     goroutine 仍会存活到业务逻辑自行返回，调用方必须保证下游传播取消信号。
func Timeout(timeout time.Duration) httpx.Middleware {
	return TimeoutWithConfig(TimeoutConfig{Timeout: timeout})
}

// TimeoutWithLogger 为整个 handler chain 提供超时控制，并复用显式注入的基础 logger。
func TimeoutWithLogger(timeout time.Duration, baseLogger logging.ILogger) httpx.Middleware {
	return TimeoutWithConfig(TimeoutConfig{Timeout: timeout, Logger: baseLogger})
}

// TimeoutWithConfig 为整个 handler chain 提供超时控制，支持并发准入上限与诊断日志。
//
// 说明：
// - MaxConcurrency > 0 时，通过信号量限制并发 handler 数量，超限请求返回 TooManyRequests；
// - 超时路径会记录一条 Warn 日志，提示 handler goroutine 可能仍在运行；
// - 该中间件通过 ctx cancellation 传递超时信号；无法强制终止不响应 ctx 的业务逻辑；
// - 超时路径不调用 TaskSupervisor.Stop，避免为忽略取消的 handler 创建无法退出的 Wait goroutine。
func TimeoutWithConfig(cfg TimeoutConfig) httpx.Middleware {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	logger := logging.ComponentLogger("gochen.http.timeout_middleware", cfg.Logger)

	var sem chan struct{}
	if cfg.MaxConcurrency > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrency)
	}

	return func(ctx httpx.IContext, next func() error) error {
		if sem != nil {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			default:
				return errors.NewCode(errors.TooManyRequests, "too many concurrent requests")
			}
		}

		if ctx == nil {
			return errors.NewCode(errors.InvalidInput, "ctx is nil")
		}
		reqCtx := ctx.RequestContext()
		if reqCtx == nil {
			return errors.NewCode(errors.InvalidInput, "request context is nil")
		}
		timeoutCtx, cancel := reqCtx.WithTimeout(timeout)
		defer cancel()

		ctx.SetContext(timeoutCtx)
		realWriter, restoreWriter, bufferWriter := installTimeoutBuffer(ctx)

		done := make(chan error, 1)
		super := task.NewTaskSupervisorWithLogger("gochen.http.timeout_middleware", logger)
		if err := super.Go(timeoutCtx, "next", func(ctx context.Context) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error(ctx, "Timeout middleware goroutine panic",
						logging.Any("panic", rec),
						logging.String("stack", string(debug.Stack())),
					)
					select {
					case done <- errors.NewCode(errors.Internal, "internal server error"):
					default:
					}
				}
			}()
			err := next()
			select {
			case done <- err:
			default:
			}
		}); err != nil {
			return err
		}

		const stopTimeout = 100 * time.Millisecond
		select {
		case err := <-done:
			if err == nil && bufferWriter != nil {
				bufferWriter.commit(realWriter)
			} else if bufferWriter != nil {
				if bufferWriter.discardBuffered() {
					resetBufferedResponseWritten(ctx)
				}
			}
			if restoreWriter != nil {
				restoreWriter()
			}
			// next() 已结束，Stop 必须是 quick path：用于等待监督 goroutine 收尾。
			stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(timeoutCtx), stopTimeout)
			defer cancelStop()
			if stopErr := super.Stop(stopCtx); stopErr != nil {
				logger.Warn(timeoutCtx, "timeout middleware stop timed out after handler completed",
					logging.String("method", ctx.Method()),
					logging.String("path", ctx.Path()),
					logging.Duration("stop_timeout", stopTimeout),
					logging.Error(stopErr),
				)
			}
			return err
		case <-timeoutCtx.Done():
			// 超时只负责取消 handler context 并返回响应。业务逻辑可能永久忽略取消，
			// 此时调用 TaskSupervisor.Stop 会额外创建一个无法退出的 Wait goroutine。
			logger.Warn(timeoutCtx, "request timed out; handler goroutine may still be running",
				logging.String("method", ctx.Method()),
				logging.String("path", ctx.Path()),
				logging.Duration("timeout", timeout),
			)
			timeoutErr := errors.NewCodeWithCause(errors.Timeout, "request timeout", timeoutCtx.Err())
			if realWriter != nil && bufferWriter != nil {
				if bufferWriter.beginTimeoutResponse() {
					writeTimeoutResponse(ctx, realWriter, timeoutErr)
				}
				return nil
			}
			return timeoutErr
		}
	}
}

func resetBufferedResponseWritten(ctx httpx.IContext) {
	if writer, ok := ctx.(responseWriterProvider); ok && writer != nil {
		writer.ResetResponseWritten()
	}
}

func installTimeoutBuffer(ctx httpx.IContext) (http.ResponseWriter, func(), *bufferedResponseWriter) {
	provider, ok := ctx.(responseWriterProvider)
	if !ok || provider == nil {
		return nil, nil, nil
	}
	realWriter := provider.ResponseWriter()
	if realWriter == nil {
		return nil, nil, nil
	}
	markWritten := provider.MarkResponseWritten
	bufferWriter := newBufferedResponseWriter(realWriter, timeoutResponseBufferLimit, markWritten)
	restore := provider.SwapResponseWriter(bufferWriter)
	return realWriter, restore, bufferWriter
}

func writeTimeoutResponse(ctx httpx.IContext, writer http.ResponseWriter, err error) {
	status, payload := httpx.EncodeErrorResponse(ctx, err)
	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"code":"INTERNAL","message":"internal server error"}`)
	}
	stateOverrider, canOverrideState := ctx.(responseStateOverrider)
	if canOverrideState && stateOverrider != nil {
		stateOverrider.OverrideResponseState(status, 0)
	} else {
		resetBufferedResponseWritten(ctx)
		ctx.SetStatus(status)
		if provider, ok := ctx.(responseWriterProvider); ok && provider != nil {
			provider.MarkResponseWritten()
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	written, _ := writer.Write(body)
	if canOverrideState && stateOverrider != nil {
		stateOverrider.OverrideResponseState(status, int64(written))
	}
}
