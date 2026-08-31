package rest

import (
	"encoding/json"
	"gochen-runtime/http/nethttp"
	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDefaultErrorHandler_MapsErrorCodesToHTTPStatus 验证 DefaultErrorHandler MapsErrorCodesToHTTPStatus。
func TestDefaultErrorHandler_MapsErrorCodesToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "conflict", err: errors.NewCode(errors.Conflict, "conflict"), wantStatus: http.StatusConflict},
		{name: "duplicate", err: errors.NewCode(errors.Duplicate, "duplicate"), wantStatus: http.StatusConflict},
		{name: "concurrency", err: errors.NewCode(errors.Concurrency, "concurrency"), wantStatus: http.StatusConflict},
		{name: "timeout", err: errors.NewCode(errors.Timeout, "timeout"), wantStatus: http.StatusRequestTimeout},
		{name: "service_unavailable", err: errors.NewCode(errors.ServiceUnavailable, "unavailable"), wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := errors.Normalize(tt.err)
			wantCode := string(errors.Code(normalized))

			wantMsg := ""
			if tt.wantStatus >= http.StatusInternalServerError {
				wantMsg = strings.ToLower(http.StatusText(tt.wantStatus))
			} else {
				var appErr *errors.AppError
				if errors.As(normalized, &appErr) && appErr != nil {
					wantMsg = appErr.Message()
				} else {
					wantMsg = normalized.Error()
				}
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			ctx, err := nethttp.NewBaseContext(rec, req)
			if err != nil {
				t.Fatalf("NewBaseContext returned error: %v", err)
			}

			// 注入关联 ID，验证错误 body 默认回传。
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

			resp := DefaultErrorHandler(ctx, tt.err)
			if resp == nil {
				t.Fatalf("expected response, got nil")
			}
			if err := resp.Send(ctx); err != nil {
				t.Fatalf("Send returned error: %v", err)
			}

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}

			var payload httpx.ResponseMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("failed to unmarshal payload: %v", err)
			}
			if payload.Code != wantCode {
				t.Fatalf("expected code %q, got %q", wantCode, payload.Code)
			}
			if payload.Message != wantMsg {
				t.Fatalf("expected message %q, got %q", wantMsg, payload.Message)
			}
			if payload.TraceID != "trc-1" {
				t.Fatalf("expected trace_id %q, got %q", "trc-1", payload.TraceID)
			}
			if payload.RequestID != "req-1" {
				t.Fatalf("expected request_id %q, got %q", "req-1", payload.RequestID)
			}
		})
	}
}
