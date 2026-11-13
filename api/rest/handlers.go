package rest

import (
	"context"
	"net/http"
	"strings"

	"gochen-runtime/http/request"
	"gochen/app/query"
	"gochen/errors"
	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleList(c httpx.IContext) error {
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if !rb.config.Query.EnablePagination {
		opts, err := parseUnpagedOptions(c, rb.config)
		if err != nil {
			return err
		}
		data, err := rb.listPage(ctx, opts)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(data)))
	}
	opts, err := rb.parsePaginationOptions(c, rb.service.MaxPageSize())
	if err != nil {
		return err
	}
	data, err := rb.listPage(ctx, opts)
	if err != nil {
		return err
	}
	total, err := rb.service.QueryCount(ctx, query.QueryOptions{Filters: opts.Filters, Advanced: opts.Advanced})
	if err != nil {
		return err
	}
	result := newPagedResult(data, total, opts.Page, opts.Size)
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(result)))
}

func (rb *RouteBuilder[T, ID]) listPage(ctx context.Context, opts *query.PaginationOptions) ([]T, error) {
	if hasConditionalQuery(opts) {
		return rb.service.Query(ctx, query.QueryOptions{
			Offset:   opts.Offset(),
			Limit:    opts.Size,
			Fields:   opts.Fields,
			Sorts:    opts.Sorts,
			Filters:  opts.Filters,
			Advanced: opts.Advanced,
		})
	}
	return rb.service.List(ctx, opts.Offset(), opts.Size)
}

func hasConditionalQuery(opts *query.PaginationOptions) bool {
	return opts != nil && (!opts.Filters.IsZero() || len(opts.Sorts) > 0 || len(opts.Fields) > 0 || !opts.Advanced.IsZero())
}

func newPagedResult[T any](data []T, total int64, page, size int) query.PagedResult[T] {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = defaultPageSizeValue
	}
	totalPages := int((total + int64(size) - 1) / int64(size))
	return query.PagedResult[T]{
		Data:       data,
		Total:      total,
		Page:       page,
		Size:       size,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1,
	}
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
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(entity)))
}

func (rb *RouteBuilder[T, ID]) handleCreate(c httpx.IContext) error {
	var entity T
	if err := bindJSONIntoEntity(c, &entity); err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}
	if err := rb.validateBody(entity); err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if err := rb.write.Create(ctx, entity); err != nil {
		return err
	}
	result := map[string]any{"id": entity.GetID()}
	status := http.StatusOK
	if rb.config.Response.UseHTTP201ForCreate {
		status = http.StatusCreated
	}
	return c.JSON(status, httpx.JSONValue(rb.config.Response.ResponseWrapper(result)))
}

func (rb *RouteBuilder[T, ID]) handleUpdate(c httpx.IContext) error {
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

	body, fields, err := readJSONBodyObject(c)
	if err != nil {
		return err
	}
	if hasManagedFields[T]() {
		if err := rejectManagedUpdateFields(fields); err != nil {
			return err
		}
	}
	expected, err := requireUint64JSONField(fields, "version")
	if err != nil {
		return err
	}
	if got := entity.GetVersion(); expected != got {
		return errors.NewCode(errors.Concurrency, "concurrency conflict").
			WithContext("id", id).
			WithContext("expected_version", expected).
			WithContext("actual_version", got)
	}
	err = bindJSONBodyIntoEntity(body, &entity)
	if err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}
	if err := ensureEntityIDMatchesPath(&entity, id); err != nil {
		return err
	}
	if err := rb.validateBody(entity); err != nil {
		return err
	}
	if err := rb.write.Update(ctx, entity); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(entity)))
}

func (rb *RouteBuilder[T, ID]) handleDelete(c httpx.IContext) error {
	if strings.TrimSpace(c.Query("purge")) != "" {
		return errors.NewCode(errors.InvalidInput, "purge query param is not supported; use DELETE /:id/purge")
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}
	if err := rb.write.Delete(ctx, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(nil)))
}

func (rb *RouteBuilder[T, ID]) validateBody(entity any) error {
	if rb.config.Body.Validator == nil {
		return nil
	}
	return rb.config.Body.Validator(entity)
}

func decodeStrictInto[T any](c httpx.IContext, target *T) error {
	body, err := c.Body()
	if err != nil {
		if errors.Is(err, errors.PayloadTooLarge) {
			return err
		}
		return errors.Wrap(err, errors.InvalidInput, "failed to read request body")
	}
	if err := request.DecodeStrictJSON(body, target); err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid request data")
	}
	return nil
}
