package nethttp

import (
	"encoding/json"
	"strings"

	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestContext helper to build a basic IContext for tests.。
//
// 说明：
//
// 参数：
// - method：参数值（具体语义见函数上下文）（类型：string）
// - target：参数值（具体语义见函数上下文）（类型：string）
//
// 返回：
// - result：返回的实例（类型：*Context）
func newTestContext(t *testing.T, method, target string) *Context {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	ctx, err := NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	return ctx
}

// TestParseInt64Param 验证路径参数 ID 解析。
func TestParseInt64Param(t *testing.T) {

	// 正常路径
	ctx := newTestContext(t, http.MethodGet, "/users/123")
	ctx.SetParam("id", "123")

	id, err := httpx.ParseInt64Param(ctx, "id")
	if err != nil {
		t.Fatalf("ParseInt64Param returned error: %v", err)
	}
	if id != 123 {
		t.Fatalf("expected id=123, got %d", id)
	}

	// 空参数
	ctxEmpty := newTestContext(t, http.MethodGet, "/users")
	_, err = httpx.ParseInt64Param(ctxEmpty, "id")
	if err == nil {
		t.Fatalf("expected error for empty id, got nil")
	}
	if code := errors.Code(err); code != errors.InvalidInput {
		t.Fatalf("expected error code %s, got %s", errors.InvalidInput, code)
	}
}

func TestContextSetParamsMergesRouteParams(t *testing.T) {
	ctx := newTestContext(t, http.MethodGet, "/users/123/orders/456")
	ctx.SetParam("tenant", "t1")
	ctx.SetParams(map[string]string{
		"id":     "123",
		"order":  "456",
		"tenant": "t2",
	})

	if got := ctx.Param("id"); got != "123" {
		t.Fatalf("expected id=123, got %q", got)
	}
	if got := ctx.Param("order"); got != "456" {
		t.Fatalf("expected order=456, got %q", got)
	}
	if got := ctx.Param("tenant"); got != "t2" {
		t.Fatalf("expected tenant=t2, got %q", got)
	}
}

// TestWriteErrorResponse 验证标准错误响应写入。
func TestWriteErrorResponse(t *testing.T) {

	ctx := newTestContext(t, http.MethodGet, "/")
	derived, err := contextx.WithRequestID(ctx.RequestContext(), "req-1")
	if err != nil {
		t.Fatalf("WithRequestID returned error: %v", err)
	}
	reqCtx := ctx.RequestContext().WithContext(derived)
	derived, err = contextx.WithTraceID(reqCtx, "trc-1")
	if err != nil {
		t.Fatalf("WithTraceID returned error: %v", err)
	}
	ctx.SetContext(reqCtx.WithContext(derived))

	// 使用预定义错误，验证状态码和 payload
	err = WriteErrorResponse(ctx, errors.NewCode(errors.NotFound, "not found"))
	if err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}

	rec := ctx.writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	var payload httpx.ResponseMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal error response message: %v", err)
	}
	if payload.Code != string(errors.NotFound) {
		t.Fatalf("expected error code %s, got %s", errors.NotFound, payload.Code)
	}
	if payload.Message == "" {
		t.Fatalf("expected non-empty error message")
	}
	if payload.TraceID != "trc-1" {
		t.Fatalf("expected trace_id %q, got %q", "trc-1", payload.TraceID)
	}
	if payload.RequestID != "req-1" {
		t.Fatalf("expected request_id %q, got %q", "req-1", payload.RequestID)
	}

	// 再次写入应被忽略（response_written 标记）
	if err := WriteErrorResponse(ctx, errors.NewCode(errors.Internal, "internal server error")); err != nil {
		t.Fatalf("second WriteErrorResponse returned error: %v", err)
	}
}

func TestWriteErrorResponse_DoesNotLeakUnknownError(t *testing.T) {
	ctx := newTestContext(t, http.MethodGet, "/")

	if err := WriteErrorResponse(ctx, errors.New("boom")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}

	rec := ctx.writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}

	var payload httpx.ResponseMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal error response message: %v", err)
	}
	if payload.Code != string(errors.Internal) {
		t.Fatalf("expected error code %s, got %s", errors.Internal, payload.Code)
	}
	if payload.Message != "internal server error" {
		t.Fatalf("expected safe message, got %q", payload.Message)
	}
}

func TestWriteErrorResponse_DoesNotLeakInternalErrorMessage(t *testing.T) {
	ctx := newTestContext(t, http.MethodGet, "/")

	if err := WriteErrorResponse(ctx, errors.NewCode(errors.Internal, "password=secret")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}

	rec := ctx.writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}

	var payload httpx.ResponseMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal error response message: %v", err)
	}
	if payload.Code != string(errors.Internal) {
		t.Fatalf("expected error code %s, got %s", errors.Internal, payload.Code)
	}
	if payload.Message != "internal server error" {
		t.Fatalf("expected safe message, got %q", payload.Message)
	}
}

type jsonFailContext struct {
	*Context
	fallbackStatus int
	fallbackText   string
}

func (c *jsonFailContext) JSON(code int, body httpx.JSONBody) error {
	return errors.New("json failed")
}

func (c *jsonFailContext) String(code int, text string) error {
	c.fallbackStatus = code
	c.fallbackText = text
	c.Set(httpContextKeyResponseWritten, httpx.ValueOf(true))
	return nil
}

func TestWriteErrorResponse_FallbackKeepsEncodedStatus(t *testing.T) {
	base := newTestContext(t, http.MethodGet, "/")
	ctx := &jsonFailContext{Context: base}

	if err := WriteErrorResponse(ctx, errors.NewCode(errors.InvalidInput, "bad request")); err != nil {
		t.Fatalf("WriteErrorResponse returned error: %v", err)
	}

	if ctx.fallbackStatus != http.StatusBadRequest {
		t.Fatalf("expected fallback status %d, got %d", http.StatusBadRequest, ctx.fallbackStatus)
	}
	if !strings.Contains(ctx.fallbackText, string(errors.InvalidInput)) {
		t.Fatalf("expected fallback text to contain error code, got %q", ctx.fallbackText)
	}
}
