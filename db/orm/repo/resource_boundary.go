package repo

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gochen/auth/scoped"
	"gochen/errors"
)

// ResolveResourceByID 在不套用 L3 DataScope、但仍强制 L2 tenant 隔离的前提下，
// 按主键解析目标资源的授权边界。
func (r *Repo[T, ID]) ResolveResourceByID(ctx context.Context, id ID) (scoped.Resource, error) {
	return r.resolveResourceByID(ctx, id, false)
}

// ResolveResourcesByID 在不套用 L3 DataScope、但仍强制 L2 tenant 隔离的前提下，
// 按主键批量解析目标资源的授权边界。
//
// ID 数量可能达到批量上限，因此按占位参数预算分片下发，避免撞驱动的
// 占位参数上限；分片只影响语句条数，返回结果仍按入参严格同序。
func (r *Repo[T, ID]) ResolveResourcesByID(ctx context.Context, ids []ID) ([]scoped.Resource, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	model, err := r.ModelFor(ctx)
	if err != nil {
		return nil, err
	}
	columns := r.resourceBoundaryColumns()
	entities := make([]T, 0, len(ids))
	for _, chunk := range chunkSlice(ids, r.maxBindParameters()) {
		query := newQueryBuilder(model, ctx).Where("id IN ?", chunk).Select(columns...)
		// 资源边界是 L3 判定的输入，永不参与跨隔离开闸：授权必须基于
		// 本隔离空间内看得到的归属事实，否则 PDP 会对不属于本空间的资源作判定。
		query, _, err = r.applyIsolation(query, isolationStrict)
		if err != nil {
			return nil, err
		}
		if r.softDelete {
			query = query.Where(r.softDeleteCols.DeletedAt + " IS NULL")
		}
		var found []T
		if err := query.Find(&found); err != nil {
			if errors.Is(err, errors.NotFound) {
				return nil, errors.NewCode(errors.NotFound, "record not found")
			}
			return nil, errors.Wrap(err, errors.Database, "failed to resolve resource boundaries")
		}
		entities = append(entities, found...)
	}
	return r.orderedResourceBoundaries(ids, entities)
}

// ResolveResourceByIDIncludingDeleted 在不套用 L3 DataScope、但仍强制 L2 tenant 隔离的
// 前提下按主键解析目标资源边界，并允许命中软删除数据。
func (r *Repo[T, ID]) ResolveResourceByIDIncludingDeleted(ctx context.Context, id ID) (scoped.Resource, error) {
	return r.resolveResourceByID(ctx, id, true)
}

func (r *Repo[T, ID]) resolveResourceByID(ctx context.Context, id ID, includeSoftDeleted bool) (scoped.Resource, error) {
	model, err := r.ModelFor(ctx)
	if err != nil {
		return scoped.Resource{}, err
	}

	entity, err := instantiateEntity[T]()
	if err != nil {
		return scoped.Resource{}, err
	}
	query := newQueryBuilder(model, ctx).Where("id = ?", id).Select(r.resourceBoundaryColumns()...)
	// 同 ResolveResourcesByID：授权输入始终严格隔离。
	query, _, err = r.applyIsolation(query, isolationStrict)
	if err != nil {
		return scoped.Resource{}, err
	}
	if r.softDelete && !includeSoftDeleted {
		query = query.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if err := query.First(entity); err != nil {
		if errors.Is(err, errors.NotFound) {
			return scoped.Resource{}, errors.NewCode(errors.NotFound, "record not found")
		}
		return scoped.Resource{}, errors.Wrap(err, errors.Database, "failed to resolve resource boundary")
	}

	return r.resourceFromEntity(entity), nil
}

func (r *Repo[T, ID]) orderedResourceBoundaries(ids []ID, entities []T) ([]scoped.Resource, error) {
	resourcesByID := make(map[string]scoped.Resource, len(entities))
	for _, entity := range entities {
		resource := r.resourceFromEntity(entity)
		resourcesByID[formatResourceID(entity.GetID())] = resource
	}
	resources := make([]scoped.Resource, 0, len(ids))
	for _, id := range ids {
		formatted := formatResourceID(id)
		resource, ok := resourcesByID[formatted]
		if !ok {
			return nil, errors.NewCode(errors.NotFound, "record not found").
				WithContext("id", formatted)
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func (r *Repo[T, ID]) resourceBoundaryColumns() []string {
	columns := []string{"id"}
	schema := r.accessSchema()
	appendIfMissing := func(column string) {
		column = strings.TrimSpace(column)
		if column == "" {
			return
		}
		for _, existing := range columns {
			if existing == column {
				return
			}
		}
		columns = append(columns, column)
	}

	appendIfMissing(schema.managedScope.column)
	appendIfMissing(schema.isolation.column)
	appendIfMissing(schema.ownerID.column)
	appendIfMissing(schema.version.column)
	return columns
}

func (r *Repo[T, ID]) resourceFromEntity(entity T) scoped.Resource {
	resource := scoped.Resource{Kind: strings.TrimSpace(r.writeResourceKind())}
	resource.ID = fmt.Sprint(entity.GetID())

	schema := r.accessSchema()
	value := reflect.ValueOf(entity)
	if schema.managedScope.column != "" {
		resource.ManagedScopeID, _ = readInt64Field(value, schema.managedScope.index)
	}
	if schema.isolation.column != "" {
		resource.TenantID, _ = readStringField(value, schema.isolation.index)
	}
	if schema.ownerID.column != "" {
		resource.OwnerID, _ = readStringField(value, schema.ownerID.index)
	}
	if schema.version.column != "" {
		resource.Revision = readRevisionField(value, schema.version.index)
	}
	resource.Kind = strings.TrimSpace(resource.Kind)
	resource.ID = strings.TrimSpace(resource.ID)
	resource.ManagedScopeID = normalizePositiveID(resource.ManagedScopeID)
	resource.TenantID = strings.TrimSpace(resource.TenantID)
	resource.OwnerID = strings.TrimSpace(resource.OwnerID)
	resource.Revision = strings.TrimSpace(resource.Revision)
	return resource
}

func readRevisionField(value reflect.Value, index []int) string {
	field, ok := resolveField(value, index)
	if !ok {
		return ""
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			return ""
		}
		field = field.Elem()
	}
	switch field.Kind() {
	case reflect.String:
		return strings.TrimSpace(field.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(field.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(field.Uint(), 10)
	default:
		return ""
	}
}

func instantiateEntity[T any]() (T, error) {
	var zero T
	typ := reflect.TypeOf(zero)
	if typ == nil {
		return zero, errors.NewCode(errors.InvalidInput, "entity type is not initialized")
	}
	var instance any
	if typ.Kind() == reflect.Ptr {
		instance = reflect.New(typ.Elem()).Interface()
	} else {
		instance = reflect.New(typ).Elem().Interface()
	}
	entity, ok := instance.(T)
	if !ok {
		return zero, errors.NewCode(errors.Internal, "failed to instantiate entity").
			WithContext("entity_type", fmt.Sprintf("%T", zero))
	}
	return entity, nil
}
