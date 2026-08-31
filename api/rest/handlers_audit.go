package rest

import (
	"net/http"

	"gochen/errors"
	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleListDeleted(c httpx.IContext) error {
	if !rb.auditedEnabled {
		return errors.NewCode(errors.NotFound, "route not enabled")
	}
	opts, err := rb.parsePaginationOptions(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	data, err := rb.auditedService.ListDeleted(ctx, opts.Offset(), opts.Size)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(data)))
}

func (rb *RouteBuilder[T, ID]) auditedUpdateBody(c httpx.IContext, entity T, id ID) ([]byte, error) {
	if !rb.auditedEnabled {
		return nil, nil
	}
	body, bodyObj, err := readJSONBodyObject(c)
	if err != nil {
		return nil, err
	}
	if err := rejectAuditedUpdateForbiddenFields(bodyObj); err != nil {
		return nil, err
	}
	wantVer, err := requireUint64JSONField(bodyObj, "version")
	if err != nil {
		return nil, err
	}
	if got := entity.GetVersion(); wantVer != got {
		return nil, errors.NewCode(errors.Concurrency, "concurrency conflict").
			WithContext("aggregate_id", id).
			WithContext("expected_version", wantVer).
			WithContext("actual_version", got)
	}
	return body, nil
}

// handleAuditTrail 查询 audited 实体的审计轨迹。
func (rb *RouteBuilder[T, ID]) handleAuditTrail(c httpx.IContext) error {
	if !rb.auditedEnabled {
		return errors.NewCode(errors.NotFound, "route not enabled")
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}

	// 复用通用分页解析，然后计算 offset/limit
	opts, err := rb.parsePaginationOptions(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	recs, err := rb.auditedService.AuditTrail(ctx, id, opts.Offset(), opts.Size)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(recs)))
}

func (rb *RouteBuilder[T, ID]) handleRestore(c httpx.IContext) error {
	if !rb.auditedEnabled {
		return errors.NewCode(errors.NotFound, "route not enabled")
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}

	ctx, operator, err := rb.mustAuditedContext(c)
	if err != nil {
		return err
	}
	if err := rb.auditedService.Restore(ctx, id, operator); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(nil)))
}

func (rb *RouteBuilder[T, ID]) handlePurge(c httpx.IContext) error {
	if !rb.auditedEnabled {
		return errors.NewCode(errors.NotFound, "route not enabled")
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}

	ctx, _, err := rb.mustAuditedContext(c)
	if err != nil {
		return err
	}
	if err := rb.auditedService.Purge(ctx, id); err != nil {
		return err
	}

	wrappedData := rb.config.Response.ResponseWrapper(nil)
	return c.JSON(http.StatusOK, httpx.JSONValue(wrappedData))
}
