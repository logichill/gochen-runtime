package repo

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gochen/auth/scoped"
	"gochen/errors"
)

// CreateWithConstraint 在显式写入约束下创建实体。
func (r *Repo[T, ID]) CreateWithConstraint(ctx context.Context, entity T, constraint scoped.WriteConstraint) error {
	if err := r.prepareCreateWithConstraint(ctx, entity, constraint); err != nil {
		return err
	}
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	if err := quer.Create(entity); err != nil {
		return writeError(err, "failed to save record")
	}
	return nil
}

// prepareCreateWithConstraint 完成约束创建的落库前准备：补齐数据域、ID 与审计字段，
// 再把约束授权的租户与范围盖到实体上。本身不触库，供单条与批量创建共用。
func (r *Repo[T, ID]) prepareCreateWithConstraint(ctx context.Context, entity T, constraint scoped.WriteConstraint) error {
	if err := r.prepareCreate(ctx, entity); err != nil {
		return err
	}
	resource, err := r.requireConstraintResource(constraint, entity.GetID(), true)
	if err != nil {
		return err
	}
	return r.applyConstraintToEntity(entity, resource)
}

// UpdateWithConstraint 在显式写入约束下更新实体。
func (r *Repo[T, ID]) UpdateWithConstraint(ctx context.Context, entity T, constraint scoped.WriteConstraint) error {
	resource, expectedVersion, err := r.requireVersionedConstraint(constraint, entity.GetID(), false)
	if err != nil {
		return err
	}
	if entity.GetVersion() != 0 && entity.GetVersion() != expectedVersion {
		// 约束 revision 取自仓储中的真实行版本，实体版本来自调用方。
		// 两者不一致意味着调用方读到的是过期快照——这是并发冲突（§4.2），
		// 不是权限问题：报 Forbidden 会让客户端以为自己无权改，从而不去重试。
		return errors.NewCode(errors.Concurrency, "concurrent modification detected").
			WithContext("resource_id", formatResourceID(entity.GetID())).
			WithContext("expected_version", expectedVersion).
			WithContext("entity_version", entity.GetVersion())
	}
	// version 省略（为 0）时以约束 revision 作为乐观锁基线：先把实体版本对齐到 expectedVersion，
	// 再交给 prepareUpdate 推进。否则 SaveWithResult 会写入实体自身的 0/递增后版本，
	// 而 WHERE 仍按真实 revision 匹配命中，导致行版本被静默回退（N→1）。
	if entity.GetVersion() == 0 {
		if err := r.alignConstraintVersion(entity, expectedVersion); err != nil {
			return err
		}
	}
	if err := r.prepareUpdate(ctx, entity); err != nil {
		return err
	}
	if err := r.applyConstraintToEntity(entity, resource); err != nil {
		return err
	}
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = quer.Where("id = ?", entity.GetID())
	quer, err = r.applyConstraintFilters(quer, resource, expectedVersion)
	if err != nil {
		return err
	}
	result, err := quer.SaveWithResult(entity)
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return errors.NewCode(errors.Unsupported, "constrained update requires an orm model with result support")
		}
		return writeError(err, "failed to update record")
	}
	affected, err := rowsAffected(result, "constrained update")
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.classifyConstraintWriteMiss(ctx, entity.GetID(), expectedVersion)
	}
	return nil
}

// DeleteWithConstraint 在显式写入约束下删除实体。
func (r *Repo[T, ID]) DeleteWithConstraint(ctx context.Context, id ID, constraint scoped.WriteConstraint) error {
	resource, expectedVersion, err := r.requireVersionedConstraint(constraint, id, false)
	if err != nil {
		return err
	}
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = quer.Where("id = ?", id)
	quer, err = r.applyConstraintFilters(quer, resource, expectedVersion)
	if err != nil {
		return err
	}
	if r.softDelete {
		nowValues, err := r.softDeleteValues(ctx)
		if err != nil {
			return err
		}
		result, err := quer.Where(r.softDeleteCols.DeletedAt + " IS NULL").UpdateValuesWithResult(nowValues)
		if err != nil {
			if errors.Is(err, errors.Unsupported) {
				return errors.NewCode(errors.Unsupported, "constrained delete requires an orm model with result support")
			}
			return errors.Wrap(err, errors.Database, "failed to delete record")
		}
		affected, err := rowsAffected(result, "constrained delete")
		if err != nil {
			return err
		}
		if affected == 0 {
			return r.classifyConstraintWriteMiss(ctx, id, expectedVersion)
		}
		return nil
	}
	result, err := quer.DeleteWithResult()
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return errors.NewCode(errors.Unsupported, "constrained delete requires an orm model with result support")
		}
		return errors.Wrap(err, errors.Database, "failed to delete record")
	}
	affected, err := rowsAffected(result, "constrained delete")
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.classifyConstraintWriteMiss(ctx, id, expectedVersion)
	}
	return nil
}

// purgeWithConstraint 在显式写入约束下执行物理删除。
func (r *Repo[T, ID]) purgeWithConstraint(ctx context.Context, id ID, constraint scoped.WriteConstraint) error {
	resource, expectedVersion, err := r.requireVersionedConstraint(constraint, id, false)
	if err != nil {
		return err
	}
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = quer.Where("id = ?", id)
	quer, err = r.applyConstraintFilters(quer, resource, expectedVersion)
	if err != nil {
		return err
	}
	result, err := quer.DeleteWithResult()
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return errors.NewCode(errors.Unsupported, "constrained purge requires an orm model with result support")
		}
		return errors.Wrap(err, errors.Database, "failed to permanently delete record")
	}
	affected, err := rowsAffected(result, "constrained purge")
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.classifyConstraintWriteMiss(ctx, id, expectedVersion)
	}
	return nil
}

func (r *Repo[T, ID]) requireConstraintResource(constraint scoped.WriteConstraint, id ID, allowGenericID bool) (scoped.ResourceConstraint, error) {
	resourceID := formatResourceID(id)
	if !allowGenericID && resourceID == "" {
		return scoped.ResourceConstraint{}, errors.NewCode(errors.InvalidInput, "resource ID is required for constrained write")
	}
	resource, err := constraint.RequireResource(r.writeResourceKind(), resourceID)
	if err != nil {
		return scoped.ResourceConstraint{}, err
	}
	if !allowGenericID && resourceID != "" && resource.ResourceID == "" {
		return scoped.ResourceConstraint{}, errors.NewCode(errors.Forbidden, "write constraint must bind to a concrete resource ID").
			WithContext("resource_kind", r.writeResourceKind()).
			WithContext("resource_id", resourceID)
	}
	return resource, nil
}

func (r *Repo[T, ID]) requireVersionedConstraint(constraint scoped.WriteConstraint, id ID, allowGenericID bool) (scoped.ResourceConstraint, uint64, error) {
	resource, err := r.requireConstraintResource(constraint, id, allowGenericID)
	if err != nil {
		return scoped.ResourceConstraint{}, 0, err
	}
	expectedVersion, err := parseConstraintRevision(resource.Revision)
	if err != nil {
		return scoped.ResourceConstraint{}, 0, err
	}
	return resource, expectedVersion, nil
}

func (r *Repo[T, ID]) applyConstraintFilters(quer *queryBuilder, constraint scoped.ResourceConstraint, expectedVersion uint64) (*queryBuilder, error) {
	schema := r.accessSchema()
	if tenantID := strings.TrimSpace(constraint.TenantID); tenantID != "" {
		if schema.isolation.column == "" {
			return nil, errors.NewCode(errors.Forbidden, "write constraint tenant_id cannot be enforced for this model")
		}
		quer = quer.Where(schema.isolation.column+" = ?", tenantID)
	}
	if constraint.ManagedScopeID != 0 {
		if schema.managedScope.column == "" {
			return nil, errors.NewCode(errors.Forbidden, "write constraint managed_scope_id cannot be enforced for this model")
		}
		quer = quer.Where(schema.managedScope.column+" = ?", constraint.ManagedScopeID)
	}
	quer = quer.Where(r.versionColumn()+" = ?", expectedVersion)
	return quer, nil
}

func (r *Repo[T, ID]) applyConstraintToEntity(entity T, constraint scoped.ResourceConstraint) error {
	schema := r.accessSchema()
	if !schema.hasConstraints() {
		return nil
	}
	if strings.TrimSpace(constraint.TenantID) != "" {
		if err := alignConstraintTenantField(entity, schema.isolation, constraint.TenantID); err != nil {
			return err
		}
	}
	if constraint.ManagedScopeID == 0 {
		return nil
	}
	if schema.managedScope.column == "" {
		return errors.NewCode(errors.Forbidden, "write constraint managed_scope_id cannot be enforced for this model")
	}
	return alignConstraintManagedScopeField(entity, schema.managedScope, constraint.ManagedScopeID)
}

// alignConstraintVersion 在 version 省略时把实体乐观锁版本对齐到约束 revision，
// 使后续 prepareUpdate 的版本推进与 WHERE 命中的真实行版本保持一致。
// 这是仓储写入约束的内部对齐动作，会直接写入可设置字段，不触发实体 setter 或业务规则。
// 调用方应确保实体 version setter 不承载必须执行的业务副作用。
func (r *Repo[T, ID]) alignConstraintVersion(entity T, expectedVersion uint64) error {
	field := r.accessSchema().version
	if !field.hasField() {
		return errors.NewCode(errors.Internal, "write constraint cannot align missing version field").
			WithContext("column", r.versionColumn())
	}
	if !setUint64Field(reflectValue(entity), field.index, expectedVersion) {
		return errors.NewCode(errors.Internal, "write constraint cannot write the entity version").
			WithContext("column", r.versionColumn())
	}
	return nil
}

func setUint64Field(value reflect.Value, index []int, target uint64) bool {
	field, ok := resolveField(value, index)
	if !ok {
		return false
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		field = field.Elem()
	}
	if !field.CanSet() {
		return false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		field.SetUint(target)
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if target > uint64(math.MaxInt64) {
			return false
		}
		field.SetInt(int64(target))
		return true
	default:
		return false
	}
}

func alignConstraintManagedScopeField[T any](entity T, field dataScopeField, target int64) error {
	if !field.hasField() {
		return errors.NewCode(errors.Forbidden, "write constraint cannot align missing entity field").
			WithContext("field", "managed_scope_id").
			WithContext("column", field.column)
	}
	current, ok := readInt64Field(reflectValue(entity), field.index)
	if ok && current != 0 && current != normalizePositiveID(target) {
		return errors.NewCode(errors.Forbidden, "write constraint does not authorize the target managed_scope_id").
			WithContext("managed_scope_id", current).
			WithContext("authorized_managed_scope_id", normalizePositiveID(target))
	}
	if !setInt64Field(reflectValue(entity), field.index, target) {
		return errors.NewCode(errors.Forbidden, "write constraint cannot write the target managed_scope_id").
			WithContext("column", field.column)
	}
	return nil
}

func alignConstraintTenantField[T any](entity T, field dataScopeField, target string) error {
	target = strings.TrimSpace(target)
	if field.column == "" {
		return errors.NewCode(errors.Forbidden, "write constraint cannot enforce tenant_id for this model").
			WithContext("column", field.column)
	}
	if !field.hasField() {
		return errors.NewCode(errors.Forbidden, "write constraint cannot align missing entity field").
			WithContext("field", "tenant_id").
			WithContext("column", field.column)
	}
	current, ok := readStringField(reflectValue(entity), field.index)
	current = strings.TrimSpace(current)
	if ok && current != "" && current != target {
		return errors.NewCode(errors.Forbidden, "write constraint does not authorize the target tenant_id").
			WithContext("tenant_id", current).
			WithContext("authorized_tenant_id", target)
	}
	if !setStringField(reflectValue(entity), field.index, target) {
		return errors.NewCode(errors.Forbidden, "write constraint cannot write the target tenant_id").
			WithContext("column", field.column)
	}
	return nil
}

func reflectValue(value any) reflect.Value { return reflect.ValueOf(value) }

func (r *Repo[T, ID]) classifyConstraintWriteMiss(ctx context.Context, id ID, expectedVersion uint64) error {
	// 极速防探测模式（§4.2）：不发探针 SELECT，按配置的固定归因直接返回。
	switch r.writeMissMode {
	case WriteMissFastNotFound:
		return errors.NewCode(errors.NotFound, "record not found").
			WithContext("id", id).
			WithContext("expected_version", expectedVersion)
	case WriteMissFastConflict:
		return errors.NewCode(errors.Conflict, "constrained write did not affect any record").
			WithContext("id", id).
			WithContext("expected_version", expectedVersion)
	}
	entity, err := r.get(ctx, id, false)
	if err == nil {
		if entity.GetVersion() != expectedVersion {
			return errors.NewCode(errors.Conflict, "record revision mismatch").
				WithContext("id", id).
				WithContext("expected_version", expectedVersion).
				WithContext("actual_version", entity.GetVersion())
		}
		return errors.NewCode(errors.Conflict, "constrained write did not affect any record").
			WithContext("id", id).
			WithContext("expected_version", expectedVersion)
	}
	if !errors.Is(err, errors.NotFound) {
		return err
	}
	notFound := errors.NewCode(errors.NotFound, "record not found").
		WithContext("id", id).
		WithContext("expected_version", expectedVersion)
	return notFound
}

func (r *Repo[T, ID]) softDeleteValues(ctx context.Context) (map[string]any, error) {
	now := time.Now()
	by := r.actorFromContext(ctx)
	values := map[string]any{r.softDeleteCols.DeletedAt: now}
	if r.softDeleteCols.DeletedBy != "" {
		values[r.softDeleteCols.DeletedBy] = by
	}
	if r.auditFields {
		values["updated_at"] = now
		values["updated_by"] = by
	}
	return values, nil
}

func (r *Repo[T, ID]) writeResourceKind() string {
	if kind := strings.TrimSpace(r.resourceKind); kind != "" {
		return kind
	}
	return strings.TrimSpace(r.meta.Table)
}

func parseConstraintRevision(revision string) (uint64, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return 0, errors.NewCode(errors.InvalidInput, "write constraint revision is required")
	}
	version, err := strconv.ParseUint(revision, 10, 64)
	if err != nil {
		return 0, errors.NewCode(errors.InvalidInput, "write constraint revision must be an unsigned integer").
			WithContext("revision", revision)
	}
	return version, nil
}

func formatResourceID[ID comparable](id ID) string {
	var zero ID
	if id == zero {
		return ""
	}
	return fmt.Sprint(id)
}
