package nethttp

// Context 内部使用的 key（避免散落魔法字符串）。
const (
	httpContextKeyRequestBodyCache = "request_body_cache"
	httpContextKeyResponseWritten  = "response_written"
)
