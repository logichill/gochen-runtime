package nethttp

import (
	"net/http"

	"gochen/httpx"
)

// IResponseWriterProvider 暴露 net/http ResponseWriter 访问能力，仅供 nethttp 适配层使用。
type IResponseWriterProvider interface {
	ResponseWriter() http.ResponseWriter
}

// IRequestProvider 暴露底层 net/http Request 访问能力，仅供确实依赖适配器细节的调用方使用。
type IRequestProvider interface {
	Request() *http.Request
}

// IHandlerProvider 暴露底层 http.Handler 访问能力，仅供 nethttp 适配层使用。
type IHandlerProvider interface {
	Handler() http.Handler
}

// IServeMuxProvider 暴露底层 *http.ServeMux 访问能力，仅供 nethttp 适配层使用。
type IServeMuxProvider interface {
	ServeMux() *http.ServeMux
}

// ResponseWriterOf 从抽象上下文中提取 net/http ResponseWriter。
func ResponseWriterOf(ctx httpx.IContext) (http.ResponseWriter, bool) {
	provider, ok := ctx.(IResponseWriterProvider)
	if !ok || provider == nil {
		return nil, false
	}
	writer := provider.ResponseWriter()
	return writer, writer != nil
}

// RequestOf 从抽象上下文中提取底层 *http.Request。
func RequestOf(ctx httpx.IContext) (*http.Request, bool) {
	provider, ok := ctx.(IRequestProvider)
	if !ok || provider == nil {
		return nil, false
	}
	request := provider.Request()
	return request, request != nil
}

// HandlerOf 从抽象 server 中提取底层 http.Handler。
func HandlerOf(server httpx.IServer) (http.Handler, bool) {
	provider, ok := server.(IHandlerProvider)
	if !ok || provider == nil {
		return nil, false
	}
	handler := provider.Handler()
	return handler, handler != nil
}

// ServeMuxOf 从抽象 server 中提取底层 *http.ServeMux。
func ServeMuxOf(server httpx.IServer) (*http.ServeMux, bool) {
	provider, ok := server.(IServeMuxProvider)
	if !ok || provider == nil {
		return nil, false
	}
	mux := provider.ServeMux()
	return mux, mux != nil
}
