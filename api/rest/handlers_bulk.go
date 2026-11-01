package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"gochen-runtime/http/request"
	"gochen/app/crud"
	"gochen/errors"
	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleCreateBulk(c httpx.IContext) error {
	var entities []T
	if err := decodeStrictInto(c, &entities); err != nil {
		return err
	}
	if err := rb.validateBulkSize(len(entities)); err != nil {
		return err
	}
	if err := rejectNilEntities(entities); err != nil {
		return err
	}
	if err := rb.validateBulkEntities(entities); err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	created, err := rb.service.ExecuteNewBulk(ctx, crud.ActionCreate, func(context.Context) ([]T, error) {
		return entities, nil
	})
	if err != nil {
		return err
	}
	ids := make([]ID, 0, len(created))
	for _, entity := range created {
		ids = append(ids, entity.GetID())
	}
	result := map[string]any{"count": len(created), "ids": ids}
	status := http.StatusOK
	if rb.config.Response.UseHTTP201ForCreate {
		status = http.StatusCreated
	}
	return c.JSON(status, httpx.JSONValue(rb.config.Response.ResponseWrapper(result)))
}

func (rb *RouteBuilder[T, ID]) handleUpdateBulk(c httpx.IContext) error {
	body, err := c.Body()
	if err != nil {
		return err
	}
	var entities []T
	if err := request.DecodeStrictJSON(body, &entities); err != nil {
		return err
	}
	if err := rb.validateBulkSize(len(entities)); err != nil {
		return err
	}
	if err := rejectNilEntities(entities); err != nil {
		return err
	}
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid bulk update body")
	}
	versions := make(map[ID]uint64, len(entities))
	for i, entity := range entities {
		if rb.isAudited() {
			if err := rejectAuditedUpdateForbiddenFields(fields[i]); err != nil {
				return err
			}
		}
		hasVersion := rb.isAudited()
		for key := range fields[i] {
			if strings.EqualFold(key, "version") {
				hasVersion = true
			}
		}
		if hasVersion {
			version, err := requireUint64JSONField(fields[i], "version")
			if err != nil {
				return err
			}
			versions[entity.GetID()] = version
		}
	}
	if err := rb.validateBulkEntities(entities); err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	incoming := make(map[ID]T, len(entities))
	ids := make([]ID, 0, len(entities))
	for _, entity := range entities {
		id := entity.GetID()
		if _, exists := incoming[id]; exists {
			return errors.NewCode(errors.InvalidInput, "duplicate id in bulk").WithContext("id", id)
		}
		incoming[id] = entity
		ids = append(ids, id)
	}
	updated, err := rb.service.ExecuteBulk(ctx, crud.ActionUpdate, ids, func(_ context.Context, entity T) error {
		source, ok := incoming[entity.GetID()]
		if !ok {
			return errors.NewCode(errors.Internal, "bulk update lost its target")
		}
		if version, supplied := versions[entity.GetID()]; supplied && version != entity.GetVersion() {
			return errors.NewCode(errors.Concurrency, "entity version mismatch").WithContext("id", entity.GetID()).WithContext("expected_version", version).WithContext("actual_version", entity.GetVersion())
		}
		mergeWritableFields(entity, source)
		return nil
	})
	if err != nil {
		return err
	}
	result := map[string]any{"count": len(updated)}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(result)))
}

func (rb *RouteBuilder[T, ID]) handleDeleteBulk(c httpx.IContext) error {
	var req struct {
		IDs []ID `json:"ids"`
	}
	if err := decodeStrictInto(c, &req); err != nil {
		return err
	}
	if err := rb.validateBulkSize(len(req.IDs)); err != nil {
		return err
	}
	if len(req.IDs) == 0 {
		return errors.NewCode(errors.InvalidInput, "bulk delete ids cannot be empty")
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if err := rb.service.DeleteBulk(ctx, req.IDs); err != nil {
		return err
	}
	result := map[string]any{"count": len(req.IDs)}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(result)))
}

func (rb *RouteBuilder[T, ID]) validateBulkEntities(entities []T) error {
	if rb.config.Body.Validator == nil {
		return nil
	}
	for i, entity := range entities {
		if err := rb.config.Body.Validator(entity); err != nil {
			return errors.Wrap(err, errors.Code(err), "invalid request data").WithContext("index", i)
		}
	}
	return nil
}

func (rb *RouteBuilder[T, ID]) validateBulkSize(size int) error {
	limit := rb.config.Body.MaxBulkSize
	if limit <= 0 {
		limit = defaultMaxBulkSize
	}
	if size > limit {
		return errors.NewCode(errors.InvalidInput, "bulk size exceeds HTTP limit").
			WithContext("max_bulk_size", limit)
	}
	if appLimit := rb.service.MaxBulkSize(); appLimit > 0 && size > appLimit {
		return errors.NewCode(errors.InvalidInput, "bulk size exceeds application limit").
			WithContext("max_bulk_size", appLimit)
	}
	return nil
}

func rejectNilEntities[T any](entities []T) error {
	for i, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr && v.IsNil() {
			return errors.NewCode(errors.InvalidInput, "entity cannot be nil").WithContext("index", i)
		}
	}
	return nil
}

// mergeWritableFields 把 incoming 的全部可写字段复制到 target。
//
// 主键、版本、访问控制角色与框架托管字段由 Application / 仓储控制，
// 传输层不能通过批量更新覆写它们。
func mergeWritableFields[T any](target T, incoming T) {
	dst := reflect.ValueOf(target)
	src := reflect.ValueOf(incoming)
	for dst.Kind() == reflect.Ptr {
		if dst.IsNil() || src.IsNil() {
			return
		}
		dst = dst.Elem()
		src = src.Elem()
	}
	if dst.Kind() != reflect.Struct || src.Kind() != reflect.Struct || dst.Type() != src.Type() {
		return
	}
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if isManagedField(field) {
			continue
		}
		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		if field.Anonymous && fieldType.Kind() == reflect.Struct {
			// 按叶子字段处理嵌入结构，保留其托管字段。
			mergeWritableFields(dst.Field(i).Addr().Interface(), src.Field(i).Addr().Interface())
			continue
		}
		value := src.Field(i)
		if !value.CanInterface() {
			continue
		}
		dst.Field(i).Set(value)
	}
}

func isManagedField(field reflect.StructField) bool {
	if field.Anonymous {
		// 嵌入的基础实体（crud.Entity / audited.AuditedEntity）整体跳过。
		return strings.HasPrefix(field.Type.String(), "crud.Entity") ||
			strings.HasPrefix(field.Type.String(), "audited.AuditedEntity")
	}
	switch field.Tag.Get("role") {
	case "id", "tenant", "scope", "owner":
		return true
	}
	if field.Tag.Get("managed") == "true" {
		return true
	}
	switch field.Name {
	case "ID", "Version", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "DeletedAt", "DeletedBy":
		return true
	}
	return false
}
