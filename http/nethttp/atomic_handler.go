package nethttp

import (
	"net/http"
	"sync/atomic"
)

type atomicHandler struct {
	current atomic.Value
}

func newAtomicHandler(initial http.Handler) *atomicHandler {
	h := &atomicHandler{}
	h.Store(initial)
	return h
}

func (h *atomicHandler) Store(next http.Handler) {
	if h == nil || next == nil {
		return
	}
	h.current.Store(next)
}

func (h *atomicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.NotFound(w, r)
		return
	}
	handler, _ := h.current.Load().(http.Handler)
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	handler.ServeHTTP(w, r)
}
