package rest

import (
	"context"

	"gochen/app/query"
	"gochen/httpx"
	"gochen/observe/logging"
)

var fallbackLogger = logging.ComponentLogger("gochen.rest.query")

// NewQueryRouteConfig 创建适用于手写路由的 QuerySchema 解析配置。
func NewQueryRouteConfig[ID comparable](schema *query.QuerySchema, defaultPageSize, maxPageSize int) *RouteConfig[ID] {
	cfg := DefaultRouteConfig[ID]()
	cfg.Query.QuerySchema = schema
	if defaultPageSize > 0 {
		cfg.Query.DefaultPageSize = defaultPageSize
	}
	if maxPageSize > 0 {
		cfg.Query.MaxPageSize = maxPageSize
	}
	return cfg
}

// ParseQueryParams 解析通用查询参数（filter/sorts/fields）。
func ParseQueryParams[ID comparable](c httpx.IContext, cfg *RouteConfig[ID]) (*query.QueryRequest, error) {
	return parseQueryParams(c, cfg)
}

// ParsePaginationOptions 解析分页查询参数（page/size），并同步解析 filter/sorts/fields 查询 DSL。
func ParsePaginationOptions[ID comparable](c httpx.IContext, cfg *RouteConfig[ID]) (*query.PaginationOptions, error) {
	return parsePaginationOptions(c, cfg)
}

// parseQueryParams 解析查询参数。
func (rb *RouteBuilder[T, ID]) parseQueryParams(c httpx.IContext) (*query.QueryRequest, error) {
	return parseQueryParams(c, rb.config)
}

// parsePaginationOptions 在计算偏移量前应用当前能力的分页上限，不影响其他能力。
func (rb *RouteBuilder[T, ID]) parsePaginationOptions(c httpx.IContext, maxPageSize int) (*query.PaginationOptions, error) {
	cfg := *rb.config
	if maxPageSize > 0 && (cfg.Query.MaxPageSize <= 0 || cfg.Query.MaxPageSize > maxPageSize) {
		cfg.Query.MaxPageSize = maxPageSize
	}
	return parsePaginationOptions(c, &cfg)
}

// AppendPaginationFilters 追加固定过滤条件，并同步补齐解码结果。
func AppendPaginationFilters(
	ctx context.Context,
	opts *query.PaginationOptions,
	schema *query.QuerySchema,
	filters ...query.Filter,
) error {
	if opts == nil || len(filters) == 0 {
		return nil
	}
	decoded, err := decodeFiltersWithFallback(ctx, schema, filters)
	if err != nil {
		return err
	}
	opts.Filters = opts.Filters.Merge(decoded)
	return nil
}

// AppendQueryFilters 追加固定过滤条件，并同步补齐解码结果。
func AppendQueryFilters(
	ctx context.Context,
	params *query.QueryRequest,
	schema *query.QuerySchema,
	filters ...query.Filter,
) error {
	if params == nil || len(filters) == 0 {
		return nil
	}
	decoded, err := decodeFiltersWithFallback(ctx, schema, filters)
	if err != nil {
		return err
	}
	params.Filters = params.Filters.Merge(decoded)
	return nil
}

// decodeFiltersWithFallback 将适配层 Filter 解码为 QueryFilters。
//
// schema 非 nil 时走 NormalizeFilters + DecodeFilters 主路径；
// schema 为 nil 时回退到 DecodeAdapterFilters 并记录告警日志。
func decodeFiltersWithFallback(ctx context.Context, schema *query.QuerySchema, filters []query.Filter) (query.QueryFilters, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	if schema != nil {
		normalized, err := schema.NormalizeFilters(filters)
		if err != nil {
			return nil, err
		}
		return schema.DecodeFilters(normalized)
	}
	fallbackLogger.Warn(ctx, "decoding filters without schema; range operators are not supported",
		logging.Int("filter_count", len(filters)))
	return query.DecodeAdapterFilters(filters)
}
