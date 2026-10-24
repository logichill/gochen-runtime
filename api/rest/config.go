// Package rest 提供 RESTful API 路由构建功能。
package rest

import (
	"gochen-runtime/http/middleware"
	"gochen/app/query"
	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/errors"
	"gochen/httpx"
)

// RoutingOptions 定义 CRUD 路由开关和路径配置。
type RoutingOptions[ID comparable] struct {
	// BasePath 是当前 CRUD 资源在路由组下的基础路径。
	BasePath string

	// IDCodec 用于解析 `:id` 路由参数并复用统一的 Bind/Scan 语义。
	//
	// 说明：
	// - 默认会尝试按 ID 的底层类型（int64/string）自动装配内置 codec；
	// - 若为 nil，则在 Build/Register 阶段 fail-fast；
	// - 对于自定义 struct 等复杂 ID，请显式提供自定义 codec。
	IDCodec codec.ICodec[ID, any]

	// EnableList 控制是否启用列表路由。
	EnableList bool

	// EnableGet 控制是否启用详情路由。
	EnableGet bool

	// EnableCreate 控制是否启用创建路由。
	EnableCreate bool

	// EnableUpdate 控制是否启用更新路由。
	EnableUpdate bool

	// EnableDelete 控制是否启用删除路由。
	EnableDelete bool

	// EnableBatch 控制是否启用批量操作路由。
	EnableBatch bool
}

// QueryOptions 定义列表查询、分页和查询 schema 配置。
type QueryOptions struct {
	// EnablePagination 控制列表路由是否启用分页查询。
	EnablePagination bool

	// MaxPageSize 是允许的最大分页大小。
	MaxPageSize int

	// DefaultPageSize 是未指定分页大小时的默认值。
	DefaultPageSize int

	// AllowedFilterFields 允许作为过滤条件的字段白名单（按“字段名”维度）。
	//
	// 说明：
	// - 为空表示不启用白名单（允许任意字段名出现在 `filter=...` 表达式中）；
	// - 过滤语法为 `filter=<field>:<op>:<value>`（可重复），白名单按 field 维度校验。
	AllowedFilterFields []string

	// AllowedSortFields 允许排序的字段白名单。
	//
	// 说明：
	// - 为空表示不启用白名单（允许任意 sort/sorts 字段）；
	// - 非空时，若请求中出现不在白名单的排序字段，框架会返回 400（fail-fast，不静默忽略）。
	AllowedSortFields []string

	// AllowedFields 允许字段选择（fields=...）的字段白名单。
	//
	// 说明：
	// - 为空表示不启用白名单（允许任意字段选择）；
	// - 非空时，若请求中出现不在白名单的字段，框架会返回 400（fail-fast，不静默忽略）。
	AllowedFields []string

	// QuerySchema 为动态查询 DSL 声明字段能力（类型/操作符/排序/投影）。
	//
	// 说明：
	// - nil 表示仅使用白名单与基础语法校验；
	// - 非 nil 时，API 层会对 filter/sorts/fields 做 schema 级校验，并对基础值做规范化；
	// - QuerySchema 与 Allowed* 可同时使用：Allowed* 用于额外收窄暴露面，QuerySchema 用于声明能力。
	QuerySchema *query.QuerySchema

	// QuerySchemaInferOptions 控制 CRUD builder / RouteBuilder 对实体 struct 的自动 schema 推导。
	//
	// 说明：
	// - 仅当 QuerySchema 为 nil 时生效；
	// - 当 AllowedFilterFields / AllowedSortFields / AllowedFields 都为空时，CRUD builder 会默认直接从实体类型 T 推导 QuerySchema；
	// - 若已手工设置 Allowed* 但仍希望启用自动推导，可显式设置该配置来强制启用；
	// - 可通过该配置覆盖字段名映射等推导细节。
	QuerySchemaInferOptions *query.SchemaInferOptions
}

// BodyOptions 定义请求体绑定与校验配置。
type BodyOptions struct {
	// MaxBatchSize 限制当前 HTTP 路由的批量元素数；<= 0 使用默认值 1000。
	// Application 的业务上限独立生效，REST 不能放宽它。
	MaxBatchSize int

	// Validator 是协议边界的请求体验证函数，仅校验绑定后的 HTTP payload。
	// 领域不变量与应用策略分别由 domain.IValidatable 和 validate.IValidator 负责。
	Validator func(any) error

	// MaxBodySize 是请求体大小限制，覆盖 JSON 与 multipart 读取。
	// > 0: 使用该限制；<= 0: 未配置或无效值，由 httpx 使用默认上限（10MB）。
	MaxBodySize int64
}

// HTTPOptions 定义 HTTP 协议层配置。
type HTTPOptions struct {
	// CORS 是跨域配置。
	CORS *middleware.CORSConfig

	// Middlewares 是追加到当前 CRUD 路由的中间件。
	Middlewares []httpx.Middleware
}

// ResponseOptions 定义响应编码与错误处理配置。
type ResponseOptions struct {
	// ErrorHandler 是自定义错误处理器。
	ErrorHandler func(httpx.IContext, error) httpx.IResponse

	// ResponseWrapper 包装成功响应数据。
	ResponseWrapper func(data any) any

	// UseHTTP201ForCreate 创建资源时返回 201 Created 而非 200 OK。
	// 默认 false 以避免调用方意外依赖 201 语义（可按团队规范显式开启）。
	UseHTTP201ForCreate bool
}

// AuditOptions 定义审计和操作人提取配置。
type AuditOptions struct {
	// OperatorExtractor 从请求中提取操作人（用于审计/软删等场景）。
	//
	// 说明：
	// - 若返回 ok=false 或 operator 为空，则不注入 operator；
	// - CRUD 路由会在调用 service/repository 前派生 ctx：`gochen/contextx.WithOperator(ctx, operator)`；
	// - audited 路由的写操作要求必须能提取到 operator；框架会在 Build/Register 阶段对其进行 fail-fast 校验。
	OperatorExtractor func(httpx.IContext) (operator string, ok bool)
}

// RouteConfig 路由配置。
type RouteConfig[ID comparable] struct {
	// Routing 配置路径、ID codec 和 CRUD 路由开关。
	Routing RoutingOptions[ID]

	// Query 配置列表查询、分页和查询 schema。
	Query QueryOptions

	// Body 配置请求体大小限制与 API 层校验。
	Body BodyOptions

	// HTTP 配置 CORS 与路由中间件。
	HTTP HTTPOptions

	// Response 配置错误处理与成功响应包装。
	Response ResponseOptions

	// Audit 配置审计操作人提取。
	Audit AuditOptions
}

const defaultMaxPageSize = 1000

const defaultMaxBatchSize = 1000

const defaultPageSizeValue = 10

const defaultMaxBodySize = httpx.DefaultMaxBodySizeBytes

// DefaultRouteConfig 默认路由配置。
func DefaultRouteConfig[ID comparable]() *RouteConfig[ID] {
	cfg := &RouteConfig[ID]{
		Routing: RoutingOptions[ID]{
			EnableList:   true,
			EnableGet:    true,
			EnableCreate: true,
			EnableUpdate: true,
			EnableDelete: true,
			// 批量路由需要 application 具备批量写能力；默认关闭，避免能力不足时 Build 失败。
			// 通过 WithBatchOperations 显式开启。
			EnableBatch: false,
		},
		Query: QueryOptions{
			EnablePagination: true,
			MaxPageSize:      defaultMaxPageSize,
			DefaultPageSize:  defaultPageSizeValue,
		},
		Body: BodyOptions{
			MaxBatchSize: defaultMaxBatchSize,
			MaxBodySize:  defaultMaxBodySize,
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
