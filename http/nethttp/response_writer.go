package nethttp

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"
)

// contextResponseWriter 将原生写入纳入 Context 的响应状态，避免错误处理重复写回。
type contextResponseWriter struct {
	http.ResponseWriter
	ctx         *Context
	mu          sync.Mutex
	wroteHeader bool
}

func (w *contextResponseWriter) recordHeader(code int) {
	w.wroteHeader = true
	w.ctx.mu.Lock()
	w.ctx.status = code
	w.ctx.markResponseWrittenLocked()
	w.ctx.mu.Unlock()
}

func (w *contextResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wroteHeader {
		return
	}
	w.ResponseWriter.WriteHeader(code)
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.recordHeader(code)
	}
}

func (w *contextResponseWriter) prepareWrite() {
	if w.wroteHeader {
		return
	}
	code := w.ctx.StatusCode()
	if code <= 0 {
		code = http.StatusOK
	}
	// 200 让底层 Write 自行探测 Content-Type；显式状态码则先提交。
	if code != http.StatusOK {
		w.ResponseWriter.WriteHeader(code)
	}
	w.recordHeader(code)
}

func (w *contextResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prepareWrite()
	n, err := w.ResponseWriter.Write(data)
	w.ctx.bytesWritten.Add(int64(n))
	return n, err
}

func (w *contextResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	reader, ok := w.ResponseWriter.(io.ReaderFrom)
	if !ok {
		return io.Copy(struct{ io.Writer }{w}, src)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prepareWrite()
	n, err := reader.ReadFrom(src)
	w.ctx.bytesWritten.Add(n)
	return n, err
}

func (w *contextResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *contextResponseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.wroteHeader {
		code := w.ctx.StatusCode()
		if code <= 0 {
			code = http.StatusOK
		}
		w.ResponseWriter.WriteHeader(code)
		w.recordHeader(code)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *contextResponseWriter) Flush() { _ = w.FlushError() }

func (w *contextResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.ctx.MarkResponseWritten()
	}
	return conn, rw, err
}

func (w *contextResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

// flushStatus 提交只调用 SetStatus 的空响应。
func (c *Context) flushStatus() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if c.responseWrittenLocked() {
		c.mu.Unlock()
		return
	}
	code, writer := c.status, c.writer
	if code <= 0 {
		code = http.StatusOK
	}
	c.markResponseWrittenLocked()
	c.mu.Unlock()
	writer.WriteHeader(code)
}
