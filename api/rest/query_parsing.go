package rest

import (
	"strconv"
	"strings"

	"gochen/app/query"
	"gochen/errors"
	"gochen/httpx"
)

type queryAllowLists struct {
	filterFields map[string]struct{}
	sortFields   map[string]struct{}
	fields       map[string]struct{}
	schema       *query.QuerySchema
}

// buildStringSet 构造字符串Set。
func buildStringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		set[v] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// buildAllowLists 构造AllowLists。
func buildAllowLists[ID comparable](cfg *RouteConfig[ID]) queryAllowLists {
	if cfg == nil {
		return queryAllowLists{}
	}
	return queryAllowLists{
		filterFields: buildStringSet(cfg.Query.AllowedFilterFields),
		sortFields:   buildStringSet(cfg.Query.AllowedSortFields),
		fields:       buildStringSet(cfg.Query.AllowedFields),
		schema:       cfg.Query.QuerySchema,
	}
}

func splitCommaList(value string) []string {
	var out []string
	for _, seg := range strings.Split(value, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseFilterParam 解析单个 filter 表达式。
//
// 说明：
// - 语法：
// - - 二元：<field>:<op>:<value>
// - - 一元：<field>:<op>（仅支持 is_null/not_null）
// - op：
// - - eq/ne/like/gt/gte/lt/lte/in/not_in/is_null/not_null
func parseFilterParam(raw string) (query.Filter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return query.Filter{}, errors.NewCode(errors.InvalidInput, "filter cannot be empty")
	}

	field, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return query.Filter{}, errors.NewCode(errors.InvalidInput, "invalid filter syntax").WithContext("filter", raw)
	}
	field = strings.TrimSpace(field)
	rest = strings.TrimSpace(rest)
	if field == "" || rest == "" {
		return query.Filter{}, errors.NewCode(errors.InvalidInput, "invalid filter syntax").WithContext("filter", raw)
	}

	opStr, valueStr, hasValue := strings.Cut(rest, ":")
	opStr = strings.TrimSpace(opStr)
	valueStr = strings.TrimSpace(valueStr)

	op, ok := query.ParseFilterOp(opStr)
	if !ok {
		return query.Filter{}, errors.NewCode(errors.InvalidInput, "invalid filter operator").
			WithContext("operator", opStr).
			WithContext("filter", raw)
	}

	switch op {
	case query.FilterOpIsNull, query.FilterOpNotNull:
		if hasValue && valueStr != "" {
			return query.Filter{}, errors.NewCode(errors.InvalidInput, "unexpected filter value for unary operator").
				WithContext("operator", string(op)).
				WithContext("filter", raw)
		}
		return query.Filter{Field: field, Op: op}, nil
	case query.FilterOpIn, query.FilterOpNotIn:
		if !hasValue {
			return query.Filter{}, errors.NewCode(errors.InvalidInput, "missing filter value").
				WithContext("operator", string(op)).
				WithContext("filter", raw)
		}
		parts := splitCommaList(valueStr)
		if len(parts) == 0 {
			return query.Filter{}, errors.NewCode(errors.InvalidInput, "empty filter value list").
				WithContext("operator", string(op)).
				WithContext("filter", raw)
		}
		return query.Filter{Field: field, Op: op, Values: parts}, nil
	default:
		if !hasValue {
			return query.Filter{}, errors.NewCode(errors.InvalidInput, "missing filter value").
				WithContext("operator", string(op)).
				WithContext("filter", raw)
		}
		// value 允许为空（例如过滤空字符串）。
		return query.Filter{Field: field, Op: op, Value: valueStr}, nil
	}
}

// parseFilterParams 设置数据值。
func parseFilterParams(values []string, allowedFields map[string]struct{}) ([]query.Filter, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]query.Filter, 0, len(values))
	for _, raw := range values {
		f, err := parseFilterParam(raw)
		if err != nil {
			return nil, err
		}
		if len(allowedFields) > 0 {
			if _, ok := allowedFields[f.Field]; !ok {
				return nil, errors.NewCode(errors.InvalidInput, "invalid filter field").WithContext("field", f.Field)
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseSortDirectionStrict 解析排序DirectionStrict。
func parseSortDirectionStrict(raw string) (query.SortDirection, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return query.ASC, errors.NewCode(errors.InvalidInput, "sort direction cannot be empty")
	}
	if d, ok := query.ParseSortDirection(raw); ok {
		return d, nil
	}
	return query.ASC, errors.NewCode(errors.InvalidInput, "invalid sort direction").WithContext("direction", raw)
}

// parseSortsParam 解析SortsParam。
func parseSortsParam(values []string) ([]query.Sort, error) {
	var out []query.Sort
	seen := map[string]int{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, seg := range strings.Split(raw, ",") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			field, dirStr, hasDir := strings.Cut(seg, ":")
			field = strings.TrimSpace(field)
			if field == "" {
				return nil, errors.NewCode(errors.InvalidInput, "invalid sorts syntax").WithContext("sort", seg)
			}

			dir := query.ASC
			if hasDir {
				parsed, err := parseSortDirectionStrict(dirStr)
				if err != nil {
					return nil, errors.Wrap(err, errors.InvalidInput, "invalid sorts syntax").
						WithContext("sort", seg).
						WithContext("direction", strings.TrimSpace(dirStr))
				}
				dir = parsed
			}

			if idx, ok := seen[field]; ok {
				out[idx].Direction = dir
				continue
			}
			seen[field] = len(out)
			out = append(out, query.Sort{Field: field, Direction: dir})
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateAllowedSorts 校验排序字段是否在白名单中；空白名单表示不限制。
func validateAllowedSorts(sorts []query.Sort, allowed map[string]struct{}) error {
	if len(sorts) == 0 || len(allowed) == 0 {
		return nil
	}
	for _, s := range sorts {
		if _, ok := allowed[s.Field]; !ok {
			return errors.NewCode(errors.InvalidInput, "invalid sort field").WithContext("field", s.Field)
		}
	}
	return nil
}

// validateSchemaSorts 校验 schema 中排序字段是否声明为可排序；空白 schema 表示不限制。
func validateSchemaSorts(sorts []query.Sort, schema *query.QuerySchema) error {
	if len(sorts) == 0 || schema == nil {
		return nil
	}
	for _, s := range sorts {
		field, ok := schema.Field(s.Field)
		if !ok || !field.Sortable {
			return errors.NewCode(errors.InvalidInput, "invalid sort field").WithContext("field", s.Field)
		}
	}
	return nil
}

// parseFieldsParam 解析字段集合Param。
func parseFieldsParam(values []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, seg := range strings.Split(raw, ",") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			if _, ok := seen[seg]; ok {
				continue
			}
			seen[seg] = struct{}{}
			out = append(out, seg)
		}
	}
	return out
}

// validateAllowedFields 校验Allowed字段集合。
func validateAllowedFields(fields []string, allowed map[string]struct{}) error {
	if len(fields) == 0 || len(allowed) == 0 {
		return nil
	}
	for _, f := range fields {
		if _, ok := allowed[f]; ok {
			continue
		}
		return errors.NewCode(errors.InvalidInput, "invalid field selection").WithContext("field", f)
	}
	return nil
}

// parseQueryCommon 解析统一查询请求。
func parseQueryCommon(c httpx.IContext, allow queryAllowLists) (request query.QueryRequest, err error) {
	if c == nil {
		return query.QueryRequest{}, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	params := c.QueryParams()
	var (
		filters []query.Filter
		sorts   []query.Sort
		fields  []string
	)

	// 新语法：filter=field:op:value（可重复）
	if rawFilters, ok := params["filter"]; ok && len(rawFilters) > 0 {
		parsed, parseErr := parseFilterParams(rawFilters, allow.filterFields)
		if parseErr != nil {
			return query.QueryRequest{}, parseErr
		}
		filters = parsed
	}

	// sorts=field:asc|desc[,field2:asc]
	if rawSorts, ok := params["sorts"]; ok && len(rawSorts) > 0 {
		parsed, parseErr := parseSortsParam(rawSorts)
		if parseErr != nil {
			return query.QueryRequest{}, parseErr
		}
		if err := validateAllowedSorts(parsed, allow.sortFields); err != nil {
			return query.QueryRequest{}, err
		}
		sorts = parsed
	}

	if rawFields, ok := params["fields"]; ok && len(rawFields) > 0 {
		parsed := parseFieldsParam(rawFields)
		if parseErr := validateAllowedFields(parsed, allow.fields); parseErr != nil {
			return query.QueryRequest{}, parseErr
		}
		fields = parsed
	}

	request = query.QueryRequest{
		Sorts:  sorts,
		Fields: fields,
	}
	request.Filters, err = decodeFiltersWithFallback(c.RequestContext(), allow.schema, filters)
	if err != nil {
		return query.QueryRequest{}, err
	}
	if allow.schema != nil {
		if err := validateSchemaSorts(sorts, allow.schema); err != nil {
			return query.QueryRequest{}, err
		}
		request.Sorts = sorts
		if err := allow.schema.ValidateFields(fields); err != nil {
			return query.QueryRequest{}, err
		}
	}

	return request, nil
}

// parseQueryParams 解析查询Params。
func parseQueryParams[ID comparable](c httpx.IContext, cfg *RouteConfig[ID]) (*query.QueryRequest, error) {
	if c == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	allow := buildAllowLists(cfg)
	request, err := parseQueryCommon(c, allow)
	if err != nil {
		return nil, err
	}
	return &request, nil
}

// parsePaginationOptions 解析Pagination选项。
func parsePaginationOptions[ID comparable](c httpx.IContext, cfg *RouteConfig[ID]) (*query.PaginationOptions, error) {
	if c == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	defaultPageSize := defaultPageSizeValue
	maxPageSize := 0
	if cfg != nil {
		if cfg.Query.DefaultPageSize > 0 {
			defaultPageSize = cfg.Query.DefaultPageSize
		}
		if cfg.Query.MaxPageSize > 0 {
			maxPageSize = cfg.Query.MaxPageSize
		}
	}

	options := &query.PaginationOptions{
		Page: 1,
		Size: defaultPageSize,
	}

	// 解析页码
	if pageStr := c.Query("page"); pageStr != "" {
		page, err := strconv.Atoi(pageStr)
		if err != nil || page < 1 {
			return nil, errors.NewCode(errors.InvalidInput, "invalid page").WithContext("page", pageStr)
		}
		options.Page = page
	}

	// 解析每页大小；同时支持 size 和 page_size 两种参数名，二者同时存在时 size 优先。
	sizeStr := c.Query("size")
	if sizeStr == "" {
		sizeStr = c.Query("page_size")
	}
	if sizeStr != "" {
		size, err := strconv.Atoi(sizeStr)
		if err != nil || size < 1 {
			return nil, errors.NewCode(errors.InvalidInput, "invalid size").WithContext("size", sizeStr)
		}
		options.Size = size
	}

	if maxPageSize > 0 && options.Size > maxPageSize {
		options.Size = maxPageSize
	}
	if err := options.Validate(maxPageSize); err != nil {
		return nil, err
	}

	allow := buildAllowLists(cfg)
	request, err := parseQueryCommon(c, allow)
	if err != nil {
		return nil, err
	}
	options.Filters = request.Filters.Clone()
	options.Sorts = append([]query.Sort(nil), request.Sorts...)
	options.Fields = append([]string(nil), request.Fields...)

	return options, nil
}

// parseUnpagedOptions 在关闭分页的路由上只解析过滤/排序/字段，忽略 page/size 参数。
func parseUnpagedOptions[ID comparable](c httpx.IContext, cfg *RouteConfig[ID]) (*query.PaginationOptions, error) {
	if c == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	allow := buildAllowLists(cfg)
	request, err := parseQueryCommon(c, allow)
	if err != nil {
		return nil, err
	}
	return &query.PaginationOptions{
		Page:    1,
		Filters: request.Filters.Clone(),
		Sorts:   append([]query.Sort(nil), request.Sorts...),
		Fields:  append([]string(nil), request.Fields...),
	}, nil
}
