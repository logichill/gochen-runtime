package nethttp

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"gochen/errors"
	"gochen/httpx"
)

type rejectingBodyWriter struct {
	h           http.Header
	status      int
	wroteHeader bool
	wroteBody   bool
	body        []byte
}

func (w *rejectingBodyWriter) Header() http.Header { return w.h }

func (w *rejectingBodyWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.wroteHeader = true
}

func (w *rejectingBodyWriter) Write(p []byte) (int, error) {
	w.wroteBody = true
	// 模拟 net/http：仅在 1xx/204/304 等状态下拒绝写 body。
	if !isBodyAllowedForStatus(w.status) {
		return 0, http.ErrBodyNotAllowed
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

func TestContext_String_NoContent_WithBody_FailFastAndLetsErrorChainWrite(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	// 204 响应不允许写 body：非空 body 必须 fail-fast，避免无声失败。
	if err := ctx.String(http.StatusNoContent, "should fail"); err == nil {
		t.Fatalf("expected ctx.String to return error for 204 with non-empty body")
	}
	if w.wroteHeader || w.wroteBody {
		t.Fatalf("expected no header/body to be written when ctx.String fails fast (wroteHeader=%v wroteBody=%v)", w.wroteHeader, w.wroteBody)
	}

	// fail-fast 场景下，错误写入链路应继续工作（此时响应尚未提交）。
	if err := WriteErrorResponse(ctx, errors.New("boom")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if !w.wroteHeader || !w.wroteBody {
		t.Fatalf("expected error chain to write response (wroteHeader=%v wroteBody=%v status=%d)", w.wroteHeader, w.wroteBody, w.status)
	}
}

func TestContext_String_NoContent_EmptyBody_WritesHeaderOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.String(http.StatusNoContent, ""); err != nil {
		t.Fatalf("ctx.String returned error: %v", err)
	}
	if !w.wroteHeader || w.status != http.StatusNoContent {
		t.Fatalf("expected status=%d and header written, got status=%d wroteHeader=%v", http.StatusNoContent, w.status, w.wroteHeader)
	}
	if w.wroteBody {
		t.Fatalf("expected body not to be written for 204 response")
	}

	// header 已提交：错误写入链路应被跳过，避免二次写响应。
	if err := WriteErrorResponse(ctx, errors.New("boom")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if w.wroteBody {
		t.Fatalf("expected WriteErrorResponse to be skipped when response_written is set")
	}
}

type alwaysFailWriter struct {
	h           http.Header
	status      int
	wroteHeader bool
	writeCalls  int
}

func (w *alwaysFailWriter) Header() http.Header { return w.h }

func (w *alwaysFailWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.wroteHeader = true
}

func (w *alwaysFailWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	return 0, errors.New("write failed")
}

func TestContext_String_WriteFails_MarksResponseWritten(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &alwaysFailWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.String(http.StatusOK, "ok"); err == nil {
		t.Fatalf("expected ctx.String to return error")
	}
	if !w.wroteHeader {
		t.Fatalf("expected header to be written before write failure")
	}

	// body 写失败时 header 已提交：错误链路应被跳过（否则会触发重复写入/写 header after committed）。
	if err := WriteErrorResponse(ctx, errors.New("boom")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if w.writeCalls != 1 {
		t.Fatalf("expected no additional writes after WriteErrorResponse, got writeCalls=%d", w.writeCalls)
	}
}

func TestWriteErrorResponse_JSONWriteFails_DoesNotFallbackAfterCommit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &alwaysFailWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := WriteErrorResponse(ctx, errors.NewCode(errors.NotFound, "not found")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}
	if w.writeCalls != 1 {
		t.Fatalf("expected no fallback write after committed JSON failure, got writeCalls=%d", w.writeCalls)
	}
}

func TestContext_ResponseWritesAreSerialized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	writers := []func() error{
		func() error { return ctx.JSON(http.StatusOK, httpx.JSONValue(map[string]any{"ok": true})) },
		func() error { return ctx.String(http.StatusAccepted, "accepted") },
		func() error { return ctx.Data(http.StatusCreated, "text/plain", []byte("created")) },
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var success atomic.Int32
	var conflicts atomic.Int32
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := writers[i%len(writers)]()
			if err == nil {
				success.Add(1)
				return
			}
			if errors.Is(err, errors.Conflict) {
				conflicts.Add(1)
				return
			}
			t.Errorf("unexpected response write error: %v", err)
		}(i)
	}
	close(start)
	wg.Wait()

	if success.Load() != 1 {
		t.Fatalf("expected exactly one successful response write, got %d", success.Load())
	}
	if conflicts.Load() != 29 {
		t.Fatalf("expected 29 conflict responses, got %d", conflicts.Load())
	}
	if ctx.BytesWritten() <= 0 {
		t.Fatalf("expected successful writer to record bytes")
	}
}

func TestContext_Data_NoContent_WithBody_FailFast(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.Data(http.StatusNoContent, "application/octet-stream", []byte("x")); err == nil {
		t.Fatalf("expected ctx.Data to return error for 204 with non-empty body")
	}
	if w.wroteHeader || w.wroteBody {
		t.Fatalf("expected no header/body to be written when ctx.Data fails fast (wroteHeader=%v wroteBody=%v)", w.wroteHeader, w.wroteBody)
	}
}

func TestContext_JSON_NoContent_WithBody_FailFast(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.JSON(http.StatusNoContent, httpx.JSONValue(map[string]any{"x": 1})); err == nil {
		t.Fatalf("expected ctx.JSON to return error for 204 with non-nil payload")
	}
	if w.wroteHeader || w.wroteBody {
		t.Fatalf("expected no header/body to be written when ctx.JSON fails fast (wroteHeader=%v wroteBody=%v)", w.wroteHeader, w.wroteBody)
	}
}

func TestContext_JSON_NoContent_NilPayload_WritesHeaderOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.JSON(http.StatusNoContent, httpx.JSONValue[any](nil)); err != nil {
		t.Fatalf("ctx.JSON returned error: %v", err)
	}
	if !w.wroteHeader || w.status != http.StatusNoContent {
		t.Fatalf("expected status=%d and header written, got status=%d wroteHeader=%v", http.StatusNoContent, w.status, w.wroteHeader)
	}
	if w.wroteBody {
		t.Fatalf("expected body not to be written for 204 response")
	}
}

func TestContext_String_ResetContent_WithBody_FailFast(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.String(http.StatusResetContent, "should fail"); err == nil {
		t.Fatalf("expected ctx.String to return error for 205 with non-empty body")
	}
	if w.wroteHeader || w.wroteBody {
		t.Fatalf("expected no header/body to be written when ctx.String fails fast (wroteHeader=%v wroteBody=%v)", w.wroteHeader, w.wroteBody)
	}
}

func TestContext_JSON_ResetContent_NilPayload_WritesHeaderOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.JSON(http.StatusResetContent, httpx.JSONValue[any](nil)); err != nil {
		t.Fatalf("ctx.JSON returned error: %v", err)
	}
	if !w.wroteHeader || w.status != http.StatusResetContent {
		t.Fatalf("expected status=%d and header written, got status=%d wroteHeader=%v", http.StatusResetContent, w.status, w.wroteHeader)
	}
	if w.wroteBody {
		t.Fatalf("expected body not to be written for 205 response")
	}
}

func TestContext_Data_ResetContent_WithBody_FailFast(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := &rejectingBodyWriter{h: make(http.Header)}
	ctx, err := NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := ctx.Data(http.StatusResetContent, "application/octet-stream", []byte("x")); err == nil {
		t.Fatalf("expected ctx.Data to return error for 205 with non-empty body")
	}
	if w.wroteHeader || w.wroteBody {
		t.Fatalf("expected no header/body to be written when ctx.Data fails fast (wroteHeader=%v wroteBody=%v)", w.wroteHeader, w.wroteBody)
	}
}
