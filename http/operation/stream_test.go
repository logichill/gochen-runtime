package operationhttp

import (
	"context"
	"gochen/app/operation"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen/errors"
)

func TestPrepareStreamHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	PrepareStreamHeaders(rec.Header())

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type: %s", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("unexpected cache control: %s", got)
	}
}

func TestStreamWritesSettledOperationEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	PrepareStreamHeaders(rec.Header())

	err := Stream(context.Background(), rec, rec, func(ctx context.Context) (*operation.Result, error) {
		return &operation.Result{
			Operation: operation.Operation{
				ID:     "op_stream",
				Type:   "task.complete",
				Mode:   operation.ModeTracked,
				Status: operation.StatusSettled,
			},
		}, nil
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: operation") {
		t.Fatalf("expected operation event, got %q", body)
	}
	if !strings.Contains(body, "\"status\":\"settled\"") {
		t.Fatalf("expected settled payload, got %q", body)
	}
}

func TestStreamRejectsNilLoaderResult(t *testing.T) {
	rec := httptest.NewRecorder()

	err := Stream(context.Background(), rec, rec, func(context.Context) (*operation.Result, error) {
		return nil, nil
	}, nil)
	if err == nil {
		t.Fatalf("expected nil loader result error")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got %v", err)
	}
}

func TestStreamRejectsNilContext(t *testing.T) {
	rec := httptest.NewRecorder()

	err := Stream(nil, rec, rec, func(context.Context) (*operation.Result, error) {
		return &operation.Result{}, nil
	}, nil)
	if err == nil {
		t.Fatalf("expected nil context error")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got %v", err)
	}
}
