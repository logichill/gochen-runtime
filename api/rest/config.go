// Package rest 按 Application 的 Descriptor 挂载标准 REST 路由。
package rest

import (
	"gochen-runtime/http/middleware"
	"gochen/app/query"
	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/errors"
	"gochen/httpx"
)

// RoutingOptions 定义当前资源的路由基础配置。
//
// 路由集合由 Application.Descriptor() 决定，不再提供 Enable* 开关。
type RoutingOptions[ID comparable] struct {
	// BasePath 是当前 CRUD 资源在路由组下的基础路径。
	BasePath string

	// IDCodec 用于解析 `:id` 路由参数并复用统一的 Bind/Scan 语义。
	//
	// 为零时按 ID 的底层类型（int64/string）自动装配内置 codec；
	// 自定义 struct 等复杂 ID 须显式提供。
	IDCodec codec.ICodec[ID, any]
}

// QueryOptions 定义列表查询、分页和查询 schema 配置。
type QueryOptions struct {
	// EnablePagination 控制列表路由是否启用分页查询。
	EnablePagination bool

	// MaxPageSize 是允许的最大分页大小。
	MaxPageSize int

	// DefaultPageSize 是未指定分页大小时的默认值。
	DefaultPageSize int

	// AllowedFilterFields 允许作为过滤条件的字段白名单。
	AllowedFilterFields []string

	// AllowedSortFields 允许排序的字段白名单。
	AllowedSortFields []string

	// AllowedFields 允许字段选择（fields=...）的字段白名单。
	AllowedFields []string

	// QuerySchema 为动态查询 DSL 声明字段能力（类型/操作符/排序/投影）。
	QuerySchema *query.QuerySchema

	// QuerySchemaInferOptions 控制对实体 struct 的自动 schema 推导。
	QuerySchemaInferOptions *query.SchemaInferOptions
}

// BodyOptions 定义请求体绑定与校验配置。
type BodyOptions struct {
	// MaxBulkSize 限制当前 HTTP 路由的批量元素数；<= 0 使用默认值 1000。
	// Application 的业务上限独立生效，REST 不能放宽它。
	MaxBulkSize int

	// Validator 是协议边界的请求体验证函数，仅校验绑定后的 HTTP payload。
	Validator func(any) error

	// MaxBodySize 是请求体大小限制，覆盖 JSON 与 multipart 读取。
	MaxBodySize int64
}

// HTTPOptions 定义 HTTP 协议层配置。
type HTTPOptions struct {
	// CORS 是跨域配置。
	CORS *middleware.CORSConfig

	// Middlewares 是追加到当前 REST 路由的中间件。
	Middlewares []httpx.Middleware
}

// ResponseOptions 定义响应编码与错误处理配置。
type ResponseOptions struct {
	// ErrorHandler 是自定义错误处理器。
	ErrorHandler func(httpx.IContext, error) httpx.IResponse

	// ResponseWrapper 包装成功响应数据。
	ResponseWrapper func(data any) any

	// UseHTTP201ForCreate 创建资源时返回 201 Created 而非 200 OK。
	UseHTTP201ForCreate bool
}

// AuditOptions 定义审计和操作人提取配置。
type AuditOptions struct {
	// OperatorExtractor 从请求中提取操作人。
	//
	// 提取到时 REST 会派生 ctx：`gochen/contextx.WithOperator(ctx, operator)`；
	// 启用审计记录时由用例校验 operator；删除生命周期本身不要求操作者。
	OperatorExtractor func(httpx.IContext) (operator string, ok bool)
}

// RouteConfig 路由配置。
type RouteConfig[ID comparable] struct {
	Routing  RoutingOptions[ID]
	Query    QueryOptions
	Body     BodyOptions
	HTTP     HTTPOptions
	Response ResponseOptions
	Audit    AuditOptions
}

const defaultMaxPageSize = 1000

const defaultMaxBulkSize = 1000

const defaultPageSizeValue = 10

const defaultMaxBodySize = httpx.DefaultMaxBodySizeBytes

// DefaultRouteConfig 默认路由配置。
func DefaultRouteConfig[ID comparable]() *RouteConfig[ID] {
	cfg := &RouteConfig[ID]{
		Query: QueryOptions{
			EnablePagination: true,
			MaxPageSize:      defaultMaxPageSize,
			DefaultPageSize:  defaultPageSizeValue,
		},
		Body: BodyOptions{
			MaxBulkSize: defaultMaxBulkSize,
			MaxBodySize: defaultMaxBodySize,
		},
		HTTP: HTTPOptions{
			CORS: &middleware.CORSConfig{},
		},
		Response: ResponseOptions{
			ErrorHandler:    DefaultErrorHandler,
			ResponseWrapper: DefaultResponseWrapper,
		},
	}
	if idCodec, err := idcodec.NewDefault[ID](); err == nil {
		cfg.Routing.IDCodec = idCodec
	}
	return cfg
}

// DefaultErrorHandler 默认错误处理器。
func DefaultErrorHandler(ctx httpx.IContext, err error) httpx.IResponse {
	if err == nil {
		return successResponse(nil)
	}
	status, payload := httpx.EncodeErrorResponse(ctx, err)
	return &jsonResponse{
		status: status,
		body:   payload,
	}
}

// DefaultResponseWrapper 默认响应包装器。
func DefaultResponseWrapper(data any) any {
	return httpx.NewSuccessMessage(data)
}

func ensureQuerySchemaForType[T any, ID comparable](cfg *RouteConfig[ID]) error {
	if cfg == nil {
		return nil
	}
	if cfg.Query.QuerySchema == nil && shouldAutoInferQuerySchema(cfg) {
		schema, err := query.InferQuerySchema[T](cfg.Query.QuerySchemaInferOptions)
		if err != nil {
			return errors.Wrap(err, errors.Code(err), "failed to infer query schema")
		}
		cfg.Query.QuerySchema = schema
	}
	applyQuerySchemaDefaults(cfg)
	return nil
}

func shouldAutoInferQuerySchema[ID comparable](cfg *RouteConfig[ID]) bool {
	if cfg == nil {
		return false
	}
	if cfg.Query.QuerySchemaInferOptions != nil {
		return true
	}
	return len(cfg.Query.AllowedFilterFields) == 0 && len(cfg.Query.AllowedSortFields) == 0 && len(cfg.Query.AllowedFields) == 0
}

func applyQuerySchemaDefaults[ID comparable](cfg *RouteConfig[ID]) {
	if cfg == nil || cfg.Query.QuerySchema == nil {
		return
	}
	if len(cfg.Query.AllowedFilterFields) == 0 {
		cfg.Query.AllowedFilterFields = cfg.Query.QuerySchema.FilterableFieldNames()
	}
	if len(cfg.Query.AllowedSortFields) == 0 {
		cfg.Query.AllowedSortFields = cfg.Query.QuerySchema.SortableFieldNames()
	}
	if len(cfg.Query.AllowedFields) == 0 {
		cfg.Query.AllowedFields = cfg.Query.QuerySchema.SelectableFieldNames()
	}
}
