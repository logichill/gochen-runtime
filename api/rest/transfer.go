package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	appcrud "gochen/app/crud"
	"gochen/app/operation"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	"gochen/errors"
	"gochen/httpx"
)

// Transfer 注册 POST 迁移入口；body 必须提供 expected_version 与 targets，
// Idempotency-Key 必填。Runner 与命令入口共用同一执行契约。
func Transfer[T domain.IEntity[ID], ID comparable](group httpx.IRouteGroup, application Application[T, ID], runner *operation.Runner, path, action string, options ...Option[T, ID]) error {
	if isNilService(application) || !runner.SupportsTransfer() {
		return errors.NewCode(errors.InvalidInput, "transfer route requires application and tracked idempotency runner")
	}
	transfer, ok := application.(appcrud.ITransferApplication[T, ID])
	declared, exists := application.Descriptor().Action(action)
	if !ok || !exists || declared.Kind != auth.KindTransfer || declared.ReceiveAction == "" {
		return errors.NewCode(errors.InvalidInput, "transfer route requires a declared transfer action and capability")
	}
	rb, err := NewRouteBuilder[T, ID](application, options...)
	if err != nil {
		return err
	}
	return Action(group, application, http.MethodPost, path, action, func(ctx context.Context, c httpx.IContext, _ Application[T, ID]) error {
		var input struct {
			ExpectedVersion *uint64           `json:"expected_version"`
			Targets         []auth.Assignment `json:"targets"`
		}
		if err := bindStrictJSON(c, &input); err != nil {
			return err
		}
		id, err := rb.parseID(c)
		if err != nil {
			return err
		}
		key := c.Header("Idempotency-Key")
		resource := application.Descriptor().Resource
		idText := formatTransferID(id)
		specification := &operation.Spec{Type: resource + "." + action, Mode: operation.ModeTracked,
			IdempotencyKey: key, Resource: &operation.Resource{Type: resource, ID: idText}}
		result, err := runner.ExecuteTransfer(ctx, specification, input.ExpectedVersion, func(runCtx context.Context, version uint64) (*operation.Result, error) {
			entity, err := transfer.Transfer(runCtx, action, id, version, input.Targets)
			if err != nil && !contextx.IsAfterCommitError(err) {
				return nil, err
			}
			return &operation.Result{Result: map[string]any{"id": entity.GetID(), "version": entity.GetVersion()}}, err
		})
		if err != nil && !contextx.IsAfterCommitError(err) {
			return err
		}
		if contextx.IsAfterCommitError(err) || result.Operation.Status == operation.StatusDegraded {
			c.SetHeader("X-Execution-Status", "degraded")
		}
		return c.JSON(http.StatusOK, httpx.JSONValue(result))
	}, options...)
}

func formatTransferID[ID comparable](id ID) string {
	data, _ := json.Marshal(id)
	if text, err := strconv.Unquote(string(data)); err == nil {
		return text
	}
	return string(data)
}
