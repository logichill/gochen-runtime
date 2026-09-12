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
// - 已开始的透传写入持锁到完成，确保真实 writer 不会在请求返回后继续写入。
type bufferedResponseWriter struct {
	mu          sync.Mutex
	header      http.Header
	body        bytes.Buffer
	limit       int64
	status      int
	wroteHeader bool
	closed      bool
	writeErr    error
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
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.passthrough {
		if w.dst != nil {
			if w.markWritten != nil {
				w.markWritten()
			}
			w.dst.WriteHeader(status)
		}
		return
	}

	w.writeHeaderLocked(status)
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
	defer w.mu.Unlock()

	if w.closed {
		return 0, errors.NewCode(errors.Timeout, "timeout response already closed")
	}
	if w.passthrough {
		if w.writeErr != nil {
			return 0, w.writeErr
		}
		if w.dst == nil {
			return len(data), nil
		}
		if w.markWritten != nil {
			w.markWritten()
		}
		n, err := w.dst.Write(data)
		w.writeErr = err
		return n, err
	}
	if !w.wroteHeader {
		w.writeHeaderLocked(http.StatusOK)
	}
	if w.limit > 0 && int64(w.body.Len()+len(data)) > w.limit {
		return 0, errors.NewCode(errors.PayloadTooLarge, "timeout response buffer exceeded")
	}
	return w.body.Write(data)
}

func (w *bufferedResponseWriter) commit(dst http.ResponseWriter) error {
	if dst == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.passthrough {
		return w.writeErr
	}
	header := w.header.Clone()
	status := w.status
	wroteHeader := w.wroteHeader
	copyHeaders(dst.Header(), header)
	if wroteHeader {
		dst.WriteHeader(status)
	}
	if w.body.Len() > 0 {
		_, err := w.body.WriteTo(dst)
		w.writeErr = err
		return err
	}
	return nil
}

func (w *bufferedResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *bufferedResponseWriter) FlushError() error {
	if w == nil {
		return http.ErrNotSupported
	}
	return w.flushToPassthrough(true)
}

func (w *bufferedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w == nil {
		return nil, nil, http.ErrNotSupported
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, nil, errors.NewCode(errors.Timeout, "timeout response already closed")
	}
	hijacker, ok := w.dst.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		w.body.Reset()
		w.passthrough = true
		if w.markWritten != nil {
			w.markWritten()
		}
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.NewCode(errors.Timeout, "timeout response already closed")
	}
	pusher, ok := w.dst.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *bufferedResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if w == nil {
		return 0, http.ErrNotSupported
	}
	if err := w.flushToPassthrough(false); err != nil {
		return 0, err
	}
	// 源读取可能越过请求期限；逐次通过 Write 检查关闭状态，禁止直接交给真实 writer。
	return io.Copy(struct{ io.Writer }{w}, src)
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

func (w *bufferedResponseWriter) flushToPassthrough(flush bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.NewCode(errors.Timeout, "timeout response already closed")
	}
	if w.writeErr != nil {
		return w.writeErr
	}
	dst := w.dst
	if dst == nil {
		return http.ErrNotSupported
	}
	if !w.passthrough {
		w.passthrough = true
		copyHeaders(dst.Header(), w.header)
		if w.markWritten != nil {
			w.markWritten()
		}
		if w.wroteHeader {
			dst.WriteHeader(w.status)
		} else {
			dst.WriteHeader(http.StatusOK)
		}
		if w.body.Len() > 0 {
			_, w.writeErr = w.body.WriteTo(dst)
			if w.writeErr != nil {
				return w.writeErr
			}
		}
	}
	if flush {
		w.writeErr = http.NewResponseController(dst).Flush()
	}
	return w.writeErr
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

func (w *bufferedResponseWriter) beginTimeoutResponse() (bool, error) {
	if w == nil {
		return false, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false, w.writeErr
	}
	if w.passthrough {
		w.closed = true
		w.body.Reset()
		return false, w.writeErr
	}
	w.closed = true
	w.body.Reset()
	return true, nil
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
//   - HTTP 适配器必须支持异步响应隔离；Gin 的池化上下文不支持该能力，返回 Unsupported。
//   - 已开始的网络写入由服务器 WriteTimeout 约束，中间件不会在写入途中归还真实 writer。
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
		if ctx == nil {
			return errors.NewCode(errors.InvalidInput, "ctx is nil")
		}
		reqCtx := ctx.RequestContext()
		if reqCtx == nil {
			return errors.NewCode(errors.InvalidInput, "request context is nil")
		}
		if _, hasWriter := ctx.(interface{ ResponseWriter() http.ResponseWriter }); hasWriter {
			if _, ok := ctx.(responseWriterProvider); !ok {
				return errors.NewCode(errors.Unsupported, "timeout middleware requires asynchronous response isolation")
			}
		}
		if sem != nil {
			select {
			case sem <- struct{}{}:
			default:
				return errors.NewCode(errors.TooManyRequests, "too many concurrent requests")
			}
		}

		timeoutCtx, cancel := reqCtx.WithTimeout(timeout)
		defer cancel()

		ctx.SetContext(timeoutCtx)
		realWriter, restoreWriter, bufferWriter := installTimeoutBuffer(ctx)

		done := make(chan error, 1)
		super := task.NewTaskSupervisorWithLogger("gochen.http.timeout_middleware", logger)
		if err := super.Go(timeoutCtx, "next", func(ctx context.Context) {
			// 请求超时不代表 handler 已退出；名额必须覆盖它的完整生命周期。
			if sem != nil {
				defer func() { <-sem }()
			}
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
			if sem != nil {
				<-sem
			}
			return err
		}

		const stopTimeout = 100 * time.Millisecond
		select {
		case err := <-done:
			if err == nil && bufferWriter != nil {
				err = bufferWriter.commit(realWriter)
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
				if write, err := bufferWriter.beginTimeoutResponse(); write {
					return writeTimeoutResponse(ctx, realWriter, timeoutErr)
				} else {
					return err
				}
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

func writeTimeoutResponse(ctx httpx.IContext, writer http.ResponseWriter, err error) error {
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
	written, writeErr := writer.Write(body)
	if canOverrideState && stateOverrider != nil {
		stateOverrider.OverrideResponseState(status, int64(written))
	}
	if writeErr == nil && written != len(body) {
		writeErr = io.ErrShortWrite
	}
	return writeErr
}
