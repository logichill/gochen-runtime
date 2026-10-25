package nethttp

import (
	"encoding/json"
	stdErrors "errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"gochen-runtime/http/request"
	"gochen/errors"
	"gochen/httpx"
)

// Context 内部使用的 key（避免散落魔法字符串）。
const (
	httpContextKeyRequestBodyCache = "request_body_cache"
	httpContextKeyResponseWritten  = "response_written"
)

// Context 是 `gochenhttp.IContext` 在 net/http 适配层上的实现。
type Context struct {
	// request 在 SetContext 时会替换为携带最新 context 的副本；请求元数据热路径使用快照字段读取。
	mu      sync.RWMutex
	bodyMu  sync.Mutex
	writeMu sync.Mutex
	request *http.Request
	writer  http.ResponseWriter
	reqCtx  httpx.IRequestContext
	status  int
	// bytesWritten 记录底层 writer 实际写出的响应体字节数（best-effort）。
	bytesWritten atomic.Int64
	aborted      bool
	values       map[string]any

	method         string
	path           string
	query          url.Values
	headers        http.Header
	userAgent      string
	baseRequest    *http.Request
	paramsSnapshot atomic.Value // stores map[string]string

	trustedProxyChecker func(netip.Addr) bool
}

// NewBaseContext 基于原始请求和响应对象创建适配层上下文。
func NewBaseContext(w http.ResponseWriter, r *http.Request) (*Context, error) {
	if w == nil {
		return nil, errors.NewCode(errors.InvalidInput, "w is nil")
	}
	if r == nil {
		return nil, errors.NewCode(errors.InvalidInput, "r is nil")
	}
	reqCtx, err := httpx.NewRequestContext(r.Context())
	if err != nil {
		var appErr *errors.AppError
		if stdErrors.As(err, &appErr) && appErr != nil {
			return nil, appErr.WithContext("component", "gochen.http.nethttp.context").WithContext("op", "NewBaseContext")
		}
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid request context").
			WithContext("component", "gochen.http.nethttp.context").
			WithContext("op", "NewBaseContext")
	}

	ctx := &Context{
		request:     r,
		writer:      w,
		reqCtx:      reqCtx,
		status:      http.StatusOK,
		values:      make(map[string]any),
		method:      r.Method,
		path:        r.URL.Path,
		query:       r.URL.Query(),
		headers:     cloneHeader(r.Header),
		userAgent:   r.UserAgent(),
		baseRequest: r,
	}
	ctx.paramsSnapshot.Store(map[string]string{})
	ctx.writer = &contextResponseWriter{ResponseWriter: w, ctx: ctx}
	return ctx, nil
}

func (c *Context) Method() string {
	return c.method
}

func (c *Context) Path() string {
	return c.path
}

// RoutePattern 返回本次请求命中的路由模板（如 "/items/{slug}"）。
//
// 基于 Go 1.22+ http.ServeMux 匹配后写入请求的 Pattern 字段；去掉可能存在的
// 方法前缀（注册模式形如 "GET /items/{slug}"），只保留路径模板。
// 未命中任何路由（404）或模板不可用时返回空字符串，调用方（如指标中间件）
// 应将此类请求归入固定类别，避免以客户端可控的原始路径作为标签。
func (c *Context) RoutePattern() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	req := c.baseRequest
	c.mu.RUnlock()
	if req == nil || req.Pattern == "" {
		return ""
	}
	pattern := req.Pattern
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	return pattern
}

func (c *Context) Query(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.query.Get(key)
}

func (c *Context) Param(key string) string {
	params, _ := c.paramsSnapshot.Load().(map[string]string)
	return params[key]
}

func (c *Context) Header(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.headers.Get(key)
}

// Body 返回请求体的缓存副本，必要时会先读取并缓存原始 body。
func (c *Context) Body() ([]byte, error) {
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()

	c.mu.RLock()
	if data, ok := contextValueAs[[]byte](c.values[httpContextKeyRequestBodyCache]); ok {
		c.mu.RUnlock()
		return data, nil
	}
	req := c.request
	writer := c.writer
	bodyLimit, hasBodyLimit := c.values[httpx.MaxBodySizeKey]
	c.mu.RUnlock()

	if req == nil {
		return nil, errors.NewCode(errors.InvalidInput, "request cannot be nil")
	}
	if req.Body == nil {
		return nil, errors.NewCode(errors.InvalidInput, "request body cannot be nil")
	}

	limitValues := map[string]any{}
	if hasBodyLimit {
		limitValues[httpx.MaxBodySizeKey] = bodyLimit
	}
	data, err := request.ReadBody(writer, req, limitValues, httpContextKeyRequestBodyCache)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.values[httpContextKeyRequestBodyCache] = httpx.ValueOf(data)
	c.mu.Unlock()
	return data, nil
}

// BindJSON 解析请求体 JSON 并绑定到目标对象。
func (c *Context) BindJSON(obj any) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	return request.DecodeStrictJSON(body, obj)
}

// BindQuery 把查询参数绑定到目标对象。
func (c *Context) BindQuery(obj any) error {
	c.mu.RLock()
	query := c.query
	c.mu.RUnlock()
	return request.BindQuery(obj, query)
}

// SetStatus 记录即将写回客户端的 HTTP 状态码。
func (c *Context) SetStatus(code int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.responseWrittenLocked() {
		return
	}
	c.status = code
}

// SetHeader 设置一项响应头。
func (c *Context) SetHeader(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.responseWrittenLocked() {
		return
	}
	c.writer.Header().Set(key, value)
}

// JSON 写入 JSON 响应，并在不允许返回 body 的状态码上主动失败。
func (c *Context) JSON(code int, body httpx.JSONBody) error {
	if !isBodyAllowedForStatus(code) && !body.IsNil() {
		return errors.NewCode(errors.InvalidInput, "response status does not allow body").
			WithContext("op", "Context.JSON").
			WithContext("status", code)
	}
	var data []byte
	var err error
	if isBodyAllowedForStatus(code) {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.Wrap(err, errors.Internal, "failed to serialize JSON")
		}
	}

	return c.writeResponse("application/json", code, data, "Context.JSON")
}

// String 写入纯文本响应，并避免在 204/304 等状态码下静默丢弃 body。
func (c *Context) String(code int, text string) error {
	if !isBodyAllowedForStatus(code) && text != "" {
		return errors.NewCode(errors.InvalidInput, "response status does not allow body").
			WithContext("op", "Context.String").
			WithContext("status", code).
			WithContext("body_len", len(text))
	}

	return c.writeResponse("text/plain", code, []byte(text), "Context.String")
}

// Data 写入原始字节响应并设置状态码。
func (c *Context) Data(code int, contentType string, data []byte) error {
	if !isBodyAllowedForStatus(code) && len(data) > 0 {
		return errors.NewCode(errors.InvalidInput, "response status does not allow body").
			WithContext("op", "Context.Data").
			WithContext("status", code).
			WithContext("body_len", len(data))
	}

	return c.writeResponse(contentType, code, data, "Context.Data")
}

func (c *Context) writeResponse(contentType string, code int, data []byte, op string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	if c.responseWrittenLocked() {
		c.mu.Unlock()
		return errors.NewCode(errors.Conflict, "response already written").
			WithContext("op", op)
	}
	c.setHeaderLocked("Content-Type", contentType)
	c.status = code
	writer := c.writer
	c.markResponseWrittenLocked()
	c.mu.Unlock()

	// 1xx/204/205/304 等响应不允许写入 body；此处仅提交 header，避免触发 net/http 的 ErrBodyNotAllowed。
	if !isBodyAllowedForStatus(code) {
		writer.WriteHeader(code)
		return nil
	}

	writer.WriteHeader(code)
	_, err := writer.Write(data)
	return err
}

func isBodyAllowedForStatus(code int) bool {
	return httpx.StatusAllowsBody(code)
}

// StatusCode 返回当前响应状态码（best-effort）。
func (c *Context) StatusCode() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Context) BytesWritten() int64 {
	return c.bytesWritten.Load()
}

func (c *Context) RequestContext() httpx.IRequestContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reqCtx
}

// SetContext 替换当前请求绑定的框架级上下文。
func (c *Context) SetContext(ctx httpx.IRequestContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqCtx = ctx
	if ctx != nil && c.request != nil {
		c.request = c.request.WithContext(ctx)
	}
}

func (c *Context) QueryParams() url.Values {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneValues(c.query)
}

// Set 在上下文内保存一项临时值，供后续 handler 或 middleware 读取。
func (c *Context) Set(key string, value httpx.ContextValue) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
}

// Get 读取一项上下文值，并在内部存的是裸值时自动包装为 ContextValue。
func (c *Context) Get(key string) (httpx.ContextValue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	if !ok {
		return httpx.ContextValue{}, false
	}
	if typed, ok := v.(httpx.ContextValue); ok {
		return typed, true
	}
	return httpx.ValueOf(v), true
}

// Required 获取指定 key 的值；当 key 不存在时返回 error（不再 panic）。
func (c *Context) Required(key string) (httpx.ContextValue, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if key == "" {
		return httpx.ContextValue{}, errors.NewCode(errors.InvalidInput, "key cannot be empty")
	}
	if v, ok := c.values[key]; ok {
		if typed, ok := v.(httpx.ContextValue); ok {
			return typed, nil
		}
		return httpx.ValueOf(v), nil
	}
	return httpx.ContextValue{}, errors.NewCode(errors.Internal, "missing required context value").
		WithContext("key", key)
}

// Abort 标记当前请求为已终止，阻止后续 handler 执行。
func (c *Context) Abort() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.aborted = true
}

// AbortWithStatus 提交响应状态码并标记当前请求已终止。
func (c *Context) AbortWithStatus(code int) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	c.aborted = true
	if c.responseWrittenLocked() {
		c.mu.Unlock()
		return
	}
	c.status = code
	writer := c.writer
	c.markResponseWrittenLocked()
	c.mu.Unlock()

	writer.WriteHeader(code)
}

// AbortWithStatusJSON 写入 JSON 响应后立刻中断后续处理链。
func (c *Context) AbortWithStatusJSON(code int, jsonObj httpx.JSONBody) {
	_ = c.JSON(code, jsonObj)
	c.Abort()
}

func (c *Context) IsAborted() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.aborted
}

// SaveUploadedFile 把上传文件内容复制到目标路径。
func (c *Context) SaveUploadedFile(file httpx.IUploadedFile, dst string) error {
	if dst == "" {
		return errors.NewCode(errors.InvalidInput, "destination path cannot be empty")
	}
	if file == nil {
		return errors.NewCode(errors.InvalidInput, "file cannot be nil")
	}

	src, err := file.Open()
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to open uploaded file")
	}
	defer func() { _ = src.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create destination file")
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, src); err != nil {
		return errors.Wrap(err, errors.Internal, "failed to save uploaded file")
	}
	return nil
}

// FormFile 读取 multipart 请求中的指定文件字段。
func (c *Context) FormFile(name string) (httpx.IUploadedFile, error) {
	if name == "" {
		return nil, errors.NewCode(errors.InvalidInput, "file field name cannot be empty")
	}
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()

	c.mu.RLock()
	req := c.request
	writer := c.writer
	bodyLimit, hasBodyLimit := c.values[httpx.MaxBodySizeKey]
	c.mu.RUnlock()

	if req == nil {
		return nil, errors.NewCode(errors.InvalidInput, "request cannot be nil")
	}
	if req.MultipartForm == nil {
		limitValues := map[string]any{}
		if hasBodyLimit {
			limitValues[httpx.MaxBodySizeKey] = bodyLimit
		}
		request.ApplyBodyLimit(writer, req, limitValues)
	}
	f, header, err := req.FormFile(name)
	if err != nil {
		var maxErr *http.MaxBytesError
		if stdErrors.As(err, &maxErr) {
			return nil, errors.NewCode(errors.PayloadTooLarge, "request body too large")
		}
		return nil, errors.Wrap(err, errors.InvalidInput, "failed to read form file")
	}
	_ = f.Close()
	return uploadedFile{header: header}, nil
}

// ClientIP 基于代理信任配置解析客户端真实 IP。
func (c *Context) ClientIP() string {
	return request.ResolveClientIP(c.baseRequest, c.trustedProxyChecker)
}

// UserAgent 返回请求头中的 User-Agent。
func (c *Context) UserAgent() string {
	return c.userAgent
}

// Request 返回底层 `*http.Request`，便于适配层向下游透传。
func (c *Context) Request() *http.Request {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.request
}

// ResponseWriter 返回底层 net/http ResponseWriter。
func (c *Context) ResponseWriter() http.ResponseWriter {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.writer
}

// SwapResponseWriter 临时替换底层 ResponseWriter，并返回恢复函数。
func (c *Context) SwapResponseWriter(writer http.ResponseWriter) func() {
	c.mu.Lock()
	old := c.writer
	if writer != nil {
		c.writer = writer
	}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.writer = old
	}
}

// MarkResponseWritten 标记响应已经由适配层直接写出。
func (c *Context) MarkResponseWritten() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.markResponseWrittenLocked()
}

// ResetResponseWritten 清除尚未提交的响应状态，允许错误处理链重新写回。
func (c *Context) ResetResponseWritten() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	delete(c.values, httpContextKeyResponseWritten)
	c.status = 0
	c.mu.Unlock()
	c.bytesWritten.Store(0)
}

// OverrideResponseState 原子替换中间件已接管响应后的观测状态。
// 该方法不写底层 ResponseWriter，仅用于已自行完成实际写回的适配层。
func (c *Context) OverrideResponseState(status int, bytesWritten int64) {
	if bytesWritten < 0 {
		bytesWritten = 0
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	c.status = status
	c.markResponseWrittenLocked()
	c.mu.Unlock()
	c.bytesWritten.Store(bytesWritten)
}

// SetParam 为当前上下文补充一项路由参数。
func (c *Context) SetParam(key, value string) {
	c.SetParams(map[string]string{key: value})
}

// SetParams 为当前上下文批量补充路由参数。
func (c *Context) SetParams(params map[string]string) {
	if len(params) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, _ := c.paramsSnapshot.Load().(map[string]string)
	next := make(map[string]string, len(current)+len(params))
	for k, v := range current {
		next[k] = v
	}
	for k, v := range params {
		next[k] = v
	}
	c.paramsSnapshot.Store(next)
}

func (c *Context) responseWrittenLocked() bool {
	if c == nil {
		return true
	}
	v, ok := c.values[httpContextKeyResponseWritten]
	if !ok {
		return false
	}
	cv, ok := v.(httpx.ContextValue)
	if !ok {
		cv = httpx.ValueOf(v)
	}
	written, _ := httpx.ValueAs[bool](cv)
	return written
}

func (c *Context) markResponseWrittenLocked() {
	if c != nil {
		c.values[httpContextKeyResponseWritten] = httpx.ValueOf(true)
	}
}

func (c *Context) setHeaderLocked(key, value string) {
	if c != nil && !c.responseWrittenLocked() {
		c.writer.Header().Set(key, value)
	}
}

type uploadedFile struct {
	header *multipart.FileHeader
}

func (f uploadedFile) Filename() string {
	if f.header == nil {
		return ""
	}
	return f.header.Filename
}

func (f uploadedFile) Size() int64 {
	if f.header == nil {
		return 0
	}
	return f.header.Size
}

// Header 返回上传文件 MIME 头。
func (f uploadedFile) Header() textproto.MIMEHeader {
	if f.header == nil {
		return nil
	}
	return f.header.Header
}

// Open 打开上传文件内容。
func (f uploadedFile) Open() (io.ReadCloser, error) {
	if f.header == nil {
		return nil, errors.NewCode(errors.InvalidInput, "uploaded file header is nil")
	}
	return f.header.Open()
}

func cloneHeader(header http.Header) http.Header {
	if len(header) == 0 {
		return http.Header{}
	}
	return header.Clone()
}

func cloneValues(values url.Values) url.Values {
	if len(values) == 0 {
		return url.Values{}
	}
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func contextValueAs[T any](value any) (T, bool) {
	var zero T
	if typed, ok := value.(T); ok {
		return typed, true
	}
	if wrapped, ok := value.(httpx.ContextValue); ok {
		return httpx.ValueAs[T](wrapped)
	}
	return zero, false
}
