package rest

import (
	"net/http"
	"strings"

	"gochen/errors"
	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleList(c httpx.IContext) error {
	if rb.config.Query.EnablePagination {
		return rb.handlePagedList(c)
	}

	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}

	query, err := rb.parseQueryParams(c)
	if err != nil {
		return err
	}
	result, err := rb.service.ListByQuery(ctx, query)
	if err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(result)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) handlePagedList(c httpx.IContext) error {
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}

	options, err := rb.parsePaginationOptions(c)
	if err != nil {
		return err
	}
	result, err := rb.service.ListPage(ctx, options)
	if err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(result)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) handleGet(c httpx.IContext) error {
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}

	entity, err := rb.service.Get(ctx, id)
	if err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(entity)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) handleCreate(c httpx.IContext) error {
	var entity T
	if err := bindJSONIntoEntity(c, &entity); err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}

	// 执行自定义验证
	if rb.config.Body.Validator != nil {
		if err := rb.config.Body.Validator(entity); err != nil {
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
	if err := rb.service.Create(ctx, entity); err != nil {
		return err
	}

	result := map[string]any{
		"id": entity.GetID(),
	}
	wrappedData := rb.config.Response.ResponseWrapper(result)

	// 根据配置选择返回 201 或 200
	statusCode := http.StatusOK
	if rb.config.Response.UseHTTP201ForCreate {
		statusCode = http.StatusCreated
	}
	return c.JSON(statusCode, httpx.JSONValue(wrappedData))
}

// handleUpdate 更新实体；audited 场景要求显式 version，并禁止通过更新篡改审计/软删字段。
func (rb *RouteBuilder[T, ID]) handleUpdate(c httpx.IContext) error {
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

	id, err := rb.parseID(c)
	if err != nil {
		return err
	}

	entity, err := rb.service.Get(ctx, id)
	if err != nil {
		return err
	}

	updateBody, err := rb.auditedUpdateBody(c, entity, id)
	if err != nil {
		return err
	}
	if err := rb.bindAndValidateUpdate(c, &entity, id, updateBody); err != nil {
		return err
	}

	if err := rb.service.Update(ctx, entity); err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(entity)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}

func (rb *RouteBuilder[T, ID]) bindAndValidateUpdate(c httpx.IContext, entity *T, id ID, updateBody []byte) error {
	var bindErr error
	if updateBody != nil {
		bindErr = bindJSONBodyIntoEntity(updateBody, entity)
	} else {
		bindErr = bindJSONIntoEntity(c, entity)
	}
	if bindErr != nil {
		if errors.Is(bindErr, errors.PayloadTooLarge) {
			return bindErr
		}
		return errors.Wrap(bindErr, errors.InvalidInput, "invalid request data")
	}
	if err := ensureEntityIDMatchesPath(entity, id); err != nil {
		return err
	}
	if rb.config.Body.Validator != nil {
		if err := rb.config.Body.Validator(*entity); err != nil {
			return err
		}
	}
	return nil
}

// handleDelete 删除实体；audited 场景走软删，并显式禁止用 query param 触发 purge。
func (rb *RouteBuilder[T, ID]) handleDelete(c httpx.IContext) error {
	// 明确不支持通过 query param 触发 purge，避免语义歧义与误用。
	if strings.TrimSpace(c.Query("purge")) != "" {
		return errors.NewCode(errors.InvalidInput, "purge query param is not supported; use DELETE /:id/purge")
	}

	id, err := rb.parseID(c)
	if err != nil {
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
		if err := rb.auditedService.Delete(ctx, id); err != nil {
			return err
		}
	} else if err := rb.service.Delete(ctx, id); err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(nil)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}
