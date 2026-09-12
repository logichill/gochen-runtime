package contracttest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen/errors"
	"gochen/httpx"
)

func runContext(t *testing.T, newServer func(*testing.T) IServer) {
	t.Run("StatusWithoutBody", func(t *testing.T) {
		srv := newServer(t)
		srv.GET("/route", func(ctx httpx.IContext) error { ctx.SetStatus(http.StatusNoContent); return nil })
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("status-only response: status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
	for name, write := range map[string]httpx.Handler{
		"JSONEncodingError":  func(ctx httpx.IContext) error { return ctx.JSON(http.StatusOK, httpx.JSONValue(make(chan int))) },
		"JSONBodyNotAllowed": func(ctx httpx.IContext) error { return ctx.JSON(http.StatusNoContent, httpx.JSONValue("invalid")) },
		"TextBodyNotAllowed": func(ctx httpx.IContext) error { return ctx.String(http.StatusNoContent, "invalid") },
		"DataBodyNotAllowed": func(ctx httpx.IContext) error {
			return ctx.Data(http.StatusNotModified, "text/plain", []byte("invalid"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t)
			observed := false
			srv.Use(func(ctx httpx.IContext, next func() error) error {
				if err := next(); err != nil {
					observed = true
					return ctx.String(http.StatusUnprocessableEntity, "handled")
				}
				return nil
			})
			srv.GET("/route", write)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
			if !observed || rec.Code != http.StatusUnprocessableEntity || rec.Body.String() != "handled" {
				t.Fatalf("response error was not recoverable: observed=%v status=%d body=%q", observed, rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("ResponseAlreadyWritten", func(t *testing.T) {
		srv := newServer(t)
		var second error
		srv.GET("/route", func(ctx httpx.IContext) error {
			if err := ctx.String(http.StatusAccepted, "first"); err != nil {
				return err
			}
			second = ctx.String(http.StatusOK, "second")
			return second
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
		if !errors.Is(second, errors.Conflict) || rec.Code != http.StatusAccepted || rec.Body.String() != "first" {
			t.Fatalf("duplicate write was accepted: err=%v status=%d body=%q", second, rec.Code, rec.Body.String())
		}
	})
	t.Run("WriterError", func(t *testing.T) {
		srv := newServer(t)
		failure := errors.New("connection write failed")
		var observed error
		srv.GET("/route", func(ctx httpx.IContext) error { observed = ctx.String(http.StatusOK, "body"); return observed })
		writer := &failingWriter{ResponseRecorder: httptest.NewRecorder(), err: failure}
		srv.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/route", nil))
		if !errors.Is(observed, failure) {
			t.Fatalf("write error lost: %v", observed)
		}
	})
	t.Run("RawResponseState", func(t *testing.T) {
		srv := newServer(t)
		srv.GET("/route", func(ctx httpx.IContext) error {
			writer := ctx.(interface{ ResponseWriter() http.ResponseWriter }).ResponseWriter()
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte("raw"))
			state := ctx.(interface {
				StatusCode() int
				BytesWritten() int64
			})
			if state.StatusCode() != http.StatusAccepted || state.BytesWritten() != 3 {
				t.Errorf("raw response state: status=%d bytes=%d", state.StatusCode(), state.BytesWritten())
			}
			return errors.New("error after raw response")
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/route", nil))
		if rec.Code != http.StatusAccepted || rec.Body.String() != "raw" {
			t.Fatalf("raw response rewritten: status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
	t.Run("RequestContextPropagation", func(t *testing.T) {
		srv := newServer(t)
		srv.GET("/route", func(ctx httpx.IContext) error {
			type key struct{}
			derived, cancel := context.WithCancel(context.WithValue(ctx.RequestContext(), key{}, "value"))
			reqCtx, err := httpx.NewRequestContext(derived)
			if err != nil {
				cancel()
				return err
			}
			ctx.SetContext(reqCtx)
			cancel()
			raw := ctx.(interface{ Request() *http.Request }).Request().Context()
			if raw.Value(key{}) != "value" || raw.Err() != context.Canceled {
				t.Errorf("raw request lost derived context: value=%v err=%v", raw.Value(key{}), raw.Err())
			}
			return ctx.String(http.StatusOK, "ok")
		})
		srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/route", nil))
	})
}

type failingWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *failingWriter) Write([]byte) (int, error) { return 0, w.err }
