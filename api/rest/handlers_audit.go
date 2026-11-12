package rest

import (
	"net/http"

	"gochen/httpx"
)

func (rb *RouteBuilder[T, ID]) handleListDeleted(c httpx.IContext) error {
	lifecycle, err := rb.lifecycle()
	if err != nil {
		return err
	}
	opts, err := rb.parsePaginationOptions(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	data, err := lifecycle.ListDeleted(ctx, opts.Offset(), opts.Size)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(data)))
}

func (rb *RouteBuilder[T, ID]) handleAuditTrail(c httpx.IContext) error {
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}
	opts, err := rb.parsePaginationOptions(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	records, err := rb.audit.AuditTrail(ctx, id, opts.Offset(), opts.Size)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(records)))
}

func (rb *RouteBuilder[T, ID]) handleRestore(c httpx.IContext) error {
	lifecycle, err := rb.lifecycle()
	if err != nil {
		return err
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if _, err := lifecycle.Restore(ctx, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(nil)))
}

func (rb *RouteBuilder[T, ID]) handlePurge(c httpx.IContext) error {
	lifecycle, err := rb.lifecycle()
	if err != nil {
		return err
	}
	id, err := rb.parseID(c)
	if err != nil {
		return err
	}
	ctx, err := rb.serviceContext(c)
	if err != nil {
		return err
	}
	if err := lifecycle.Purge(ctx, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpx.JSONValue(rb.config.Response.ResponseWrapper(nil)))
}
