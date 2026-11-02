package repo

import (
	"context"
	"math"
	"reflect"

	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
)

// CheckTransferEntry 在授权前拒绝上下文或仓储绑定的事务。
func (r *Repo[T, ID]) CheckTransferEntry(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	_, lifecycle := contextx.TxLifecycleFromContext(ctx)
	_, _, contextual := orm.TxSessionFromContext(ctx)
	_, bound := r.orm.(orm.IOrmSession)
	if lifecycle || contextual || bound {
		return errors.NewCode(errors.FailedPrecondition, "transfer must begin outside a transaction")
	}
	return nil
}

// Transfer 在同源事务内执行版本约束的维度写入与实际后像回读。
func (r *Repo[T, ID]) Transfer(ctx context.Context, id ID, expectedVersion uint64, changes []crud.DimensionChange, using, check spec.Spec) (crud.TransferResult[T], error) {
	var zero crud.TransferResult[T]
	if _, _, ok := orm.TxSessionFromContext(ctx); !ok {
		return zero, errors.NewCode(errors.FailedPrecondition, "transfer requires an active transaction")
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return zero, err
	}
	if !r.versioned() {
		return zero, errors.NewCode(errors.InvalidInput, "transfer requires optimistic versioning")
	}
	if expectedVersion == math.MaxUint64 || len(changes) == 0 {
		return zero, errors.NewCode(errors.InvalidInput, "invalid transfer version or empty changes")
	}
	if err := r.execution.ValidateWriteSpec(using, false); err != nil {
		return zero, err
	}
	if err := r.execution.ValidateWriteSpec(check, false); err != nil {
		return zero, err
	}
	condition, err := r.writeSpecCondition(using)
	if err != nil {
		return zero, err
	}
	query, err := r.query(ctx)
	if err != nil {
		return zero, err
	}
	query = applyCondition(r.withActive(r.withID(query, id)), condition)
	var before T
	if err := query.First(&before); err != nil {
		if errors.Is(err, errors.NotFound) {
			return zero, notFoundError(id)
		}
		return zero, errors.Wrap(err, errors.Database, "read transfer source failed")
	}
	if before.GetVersion() != expectedVersion {
		return zero, concurrencyError(id, expectedVersion)
	}
	expected, err := meta.Clone(before)
	if err != nil {
		return zero, err
	}
	seen := map[meta.Dimension]bool{}
	actualChanges := make([]crud.DimensionChange, 0, len(changes))
	values := map[string]any{}
	for _, change := range changes {
		if seen[change.Dimension] || !r.execution.CanTransfer(change.Dimension) {
			return zero, errors.NewCode(errors.InvalidInput, "transfer dimension is duplicated or lacks capability")
		}
		seen[change.Dimension] = true
		field, err := r.entityMeta.Resolve(meta.ByDimension(change.Dimension))
		if err != nil {
			return zero, err
		}
		converted, err := field.Convert(change.Value)
		if err != nil {
			return zero, err
		}
		target, ok := field.Value(expected)
		if !ok || !target.CanSet() {
			return zero, errors.NewCode(errors.InvalidInput, "transfer field is not settable")
		}
		if reflect.DeepEqual(target.Interface(), converted.Interface()) {
			continue
		}
		target.Set(converted)
		value, err := field.Read(expected)
		if err != nil {
			return zero, err
		}
		column, _ := r.resolver.columnOf(field.Name)
		values[column] = value
		actualChanges = append(actualChanges, crud.DimensionChange{Dimension: change.Dimension, Value: value})
	}
	if len(actualChanges) == 0 {
		return zero, errors.NewCode(errors.InvalidInput, "transfer has no actual dimension change")
	}
	if err := r.prepareUpdate(ctx, expected); err != nil {
		return zero, err
	}
	r.advanceVersion(expected, expectedVersion)
	if validatable, ok := any(expected).(domain.IValidatable); ok {
		if err := validatable.Validate(); err != nil {
			return zero, err
		}
	}
	if err := r.checkWriteSpec(check, expected); err != nil {
		return zero, err
	}
	for _, field := range r.entityMeta.ManagedFields() {
		if field.Identity || field.Dimension != "" || field.Name == "CreatedAt" || field.Name == "CreatedBy" || field.Name == "DeletedAt" || field.Name == "DeletedBy" {
			continue
		}
		column, exists := r.resolver.columnOf(field.Name)
		if !exists {
			continue
		}
		value, err := field.Read(expected)
		if err != nil {
			return zero, err
		}
		values[column] = value
	}
	values[r.access.version] = expectedVersion + 1
	query, err = r.query(ctx)
	if err != nil {
		return zero, err
	}
	query = applyCondition(r.withActive(r.withID(query, id)), condition).Where(r.resolver.quote(r.access.version)+" = ?", expectedVersion)
	written, err := query.UpdateValuesWithResult(values)
	if err != nil {
		return zero, writeError(err, "transfer write failed")
	}
	count, err := rowsAffected(written, "transfer")
	if err != nil {
		return zero, err
	}
	if count == 0 {
		return zero, r.classifyWriteMiss(ctx, id, condition, expectedVersion, nil)
	}
	readback, err := r.query(ctx)
	if err != nil {
		return zero, err
	}
	var after T
	if err := r.withID(readback, id).First(&after); err != nil {
		return zero, errors.Wrap(err, errors.Internal, "transfer row disappeared on readback")
	}
	if after.GetVersion() != expectedVersion+1 {
		return zero, errors.NewCode(errors.Internal, "transfer version differs from expected persisted state")
	}
	for _, field := range r.entityMeta.Fields() {
		if field.Dimension == "" {
			continue
		}
		wanted, err := field.Read(expected)
		if err != nil {
			return zero, err
		}
		actual, err := field.Read(after)
		if err != nil {
			return zero, err
		}
		if !reflect.DeepEqual(wanted, actual) {
			return zero, errors.NewCode(errors.Internal, "transfer dimension differs from expected persisted state").WithContext("dimension", string(field.Dimension))
		}
	}
	if err := r.checkWriteSpec(check, after); err != nil {
		return zero, errors.Wrap(err, errors.Internal, "persisted transfer state violates receive condition")
	}
	return crud.TransferResult[T]{Before: before, After: after, Changes: actualChanges}, nil
}
