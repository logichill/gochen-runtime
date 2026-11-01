package http

import (
	nethttp "net/http"
	"strings"
	"testing"

	"gochen/errors"
	"gochen/httpx"
)

type recordingRouteGroup struct {
	registered map[string]string
}

func newRecordingRouteGroup() *recordingRouteGroup {
	return &recordingRouteGroup{registered: make(map[string]string)}
}

func (m *recordingRouteGroup) record(method, path string) {
	m.registered[method] = path
}

func (m *recordingRouteGroup) GET(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodGet, path)
	return m
}

func (m *recordingRouteGroup) POST(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodPost, path)
	return m
}

func (m *recordingRouteGroup) PUT(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodPut, path)
	return m
}

func (m *recordingRouteGroup) DELETE(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodDelete, path)
	return m
}

func (m *recordingRouteGroup) PATCH(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodPatch, path)
	return m
}

func (m *recordingRouteGroup) HEAD(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodHead, path)
	return m
}

func (m *recordingRouteGroup) OPTIONS(path string, handler httpx.Handler) httpx.IRouteGroup {
	m.record(nethttp.MethodOptions, path)
	return m
}

func (m *recordingRouteGroup) Group(string) httpx.IRouteGroup { return m }

func (m *recordingRouteGroup) Use(...httpx.Middleware) httpx.IRouteGroup { return m }

func TestRegisterRouteRejectsNilGroupAndHandler(t *testing.T) {
	handler := func(httpx.IContext) error { return nil }
	if err := RegisterRoute(nil, nethttp.MethodGet, "/x", handler); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for nil group, got %v", err)
	}
	if err := RegisterRoute(newRecordingRouteGroup(), nethttp.MethodGet, "/x", nil); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for nil handler, got %v", err)
	}
}

func TestRegisterRouteRejectsUnsupportedMethod(t *testing.T) {
	group := newRecordingRouteGroup()
	err := RegisterRoute(group, "TRACE", "/x", func(httpx.IContext) error { return nil })
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
	if len(group.registered) != 0 {
		t.Fatalf("expected no route to be registered, got %v", group.registered)
	}
}

func TestRegisterRouteDispatchesLowercaseMethod(t *testing.T) {
	for _, method := range []string{
		nethttp.MethodGet,
		nethttp.MethodPost,
		nethttp.MethodPut,
		nethttp.MethodDelete,
		nethttp.MethodPatch,
		nethttp.MethodHead,
		nethttp.MethodOptions,
	} {
		group := newRecordingRouteGroup()
		if err := RegisterRoute(group, " "+strings.ToLower(method)+" ", "/path", func(httpx.IContext) error { return nil }); err != nil {
			t.Fatalf("unexpected error for %s: %v", method, err)
		}
		if group.registered[method] != "/path" {
			t.Fatalf("expected %s to dispatch to /path, got %q", method, group.registered[method])
		}
	}
}
