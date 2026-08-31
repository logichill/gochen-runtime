package rest

import (
	"encoding/json"
	"net/http"
	"reflect"

	"gochen-runtime/http/request"
	"gochen/errors"
	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleCreateAll(c httpx.IContext) error {
	var entities []T
	if err := bindStrictJSON(c, &entities); err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}

	// P1：拒绝 nil 元素
	if err := rejectNilEntities(entities); err != nil {
		return err
	}
	if err := rb.validateBatchEntities(entities); err != nil {
		return err
	}

	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if rb.auditedEnabled {
		auditCtx, _, err := rb.mustAuditedContext(c)
		if err != nil {
			return err
		}
		ctx = auditCtx
	}
	// 路由注册期 validateServiceCapabilities 已断言 batchWriter 存在。
	writer, _ := rb.batchWriter()
	if err := writer.CreateAll(ctx, entities); err != nil {
		return err
	}

	// 收集创建的 ID 列表
	ids := make([]ID, 0, len(entities))
	for _, entity := range entities {
		ids = append(ids, entity.GetID())
	}

	result := map[string]any{
		"count": len(entities),
		"ids":   ids,
	}
	wrappedData := rb.config.Response.ResponseWrapper(result)

	// 与单条 create 同一口径：默认 200，UseHTTP201ForCreate 显式开启时才返回 201。
	statusCode := http.StatusOK
	if rb.config.Response.UseHTTP201ForCreate {
		statusCode = http.StatusCreated
	}
	return c.JSON(statusCode, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) handleUpdateBatch(c httpx.IContext) error {
	// 先获取原始 body 用于 audited 场景的校验，再直接基于该 body 严格解码，避免依赖适配层重复读取缓存。
	body, err := c.Body()
	if err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "failed to read request body")
	}

	var entities []T
	if err := request.DecodeStrictJSON(body, &entities); err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}

	// P1：拒绝 nil 元素
	if err := rejectNilEntities(entities); err != nil {
		return err
	}
	if err := rb.validateBatchEntities(entities); err != nil {
		return err
	}

	// P0：audited 场景的字段/版本约束校验
	if rb.auditedEnabled {
		if err := validateAuditedBatchUpdate(body, entities); err != nil {
			return err
		}
	}

	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if rb.auditedEnabled {
		auditCtx, _, err := rb.mustAuditedContext(c)
		if err != nil {
			return err
		}
		ctx = auditCtx
	}
	// 路由注册期 validateServiceCapabilities 已断言 batchWriter 存在。
	writer, _ := rb.batchWriter()
	if err := writer.UpdateAll(ctx, entities); err != nil {
		return err
	}

	result := map[string]any{
		"count": len(entities),
	}
	wrappedData := rb.config.Response.ResponseWrapper(result)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) handleDeleteBatch(c httpx.IContext) error {
	var req struct {
		IDs []ID `json:"ids"`
	}

	if err := bindStrictJSON(c, &req); err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}
	if len(req.IDs) == 0 {
		return errors.NewCode(errors.InvalidInput, "batch delete ids cannot be empty")
	}

	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if rb.auditedEnabled {
		auditCtx, _, err := rb.mustAuditedContext(c)
		if err != nil {
			return err
		}
		ctx = auditCtx
	}
	// 路由注册期 validateServiceCapabilities 已断言 batchWriter 存在。
	writer, _ := rb.batchWriter()
	if err := writer.DeleteAll(ctx, req.IDs); err != nil {
		return err
	}

	result := map[string]any{
		"count": len(req.IDs),
	}
	wrappedData := rb.config.Response.ResponseWrapper(result)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) validateBatchEntities(entities []T) error {
	if rb == nil || rb.config == nil || rb.config.Body.Validator == nil {
		return nil
	}
	for i, entity := range entities {
		if err := rb.config.Body.Validator(entity); err != nil {
			return errors.Wrap(err, errors.Code(err), "invalid request data").WithContext("index", i)
		}
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

// validateAuditedBatchUpdate 校验Audited批量Update。
func validateAuditedBatchUpdate[T any](body []byte, entities []T) error {
	// 解析为 JSON 数组以便逐项校验
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid JSON array")
	}

	if len(items) != len(entities) {
		return errors.NewCode(errors.InvalidInput, "entity count mismatch")
	}

	for i, item := range items {
		// 校验必须携带 version
		if _, err := requireUint64JSONField(item, "version"); err != nil {
			return errors.Wrap(err, errors.InvalidInput, "batch update validation failed").
				WithContext("index", i)
		}

		// 校验禁止更新托管字段
		if err := rejectAuditedUpdateForbiddenFields(item); err != nil {
			return errors.Wrap(err, errors.InvalidInput, "batch update validation failed").
				WithContext("index", i)
		}
	}

	return nil
}
