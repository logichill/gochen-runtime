package nethttp

import (
	"testing"

	"gochen/httpx"
)

// P2-045/P2-075 回归：经 WithRouteRegistry 包装后的服务器仍可提取底层能力。
func TestHandlerOfAndServeMuxOfUnwrapRegistryServer(t *testing.T) {
	server := NewServer(nil)
	handler, ok := HandlerOf(server)
	if !ok || handler == nil {
		t.Fatal("expected raw server to expose http.Handler")
	}
	mux, ok := ServeMuxOf(server)
	if !ok || mux == nil {
		t.Fatal("expected raw server to expose *http.ServeMux")
	}

	wrapped := httpx.WithRouteRegistry(server)
	unwrappedHandler, ok := HandlerOf(wrapped)
	if !ok || unwrappedHandler == nil {
		t.Fatal("expected HandlerOf to unwrap route registry server and expose http.Handler")
	}
	unwrappedMux, ok := ServeMuxOf(wrapped)
	if !ok || unwrappedMux == nil {
		t.Fatal("expected ServeMuxOf to unwrap route registry server and expose *http.ServeMux")
	}
	if unwrappedMux != mux {
		t.Fatal("unwrapped ServeMux differs from raw server mux")
	}
}
