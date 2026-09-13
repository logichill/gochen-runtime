package repo

import (
	"context"
	"reflect"
	"strconv"

	"gochen/auth/scoped"
	"gochen/db/tx"
)

// createAllWithConstraint 在显式写入约束下批量创建实体。
//
// 创建路径的约束只用来给新行盖租户/范围戳——新行没有 revision 可匹配，约束不进
// WHERE。因此逐条盖完戳之后可以合并成一条多值 INSERT，与无约束 CreateAll 的语句
// 形态一致，不会因为启用 L3 就退化成 N 条单行 INSERT。
//
// Update / Delete 不能这么合并：它们每条的 WHERE 各带自己的 revision 与范围，
// 且要靠逐条 RowsAffected 才能把未命中区分成 404 / 403 / 409。
func (r *Repo[T, ID]) createAllWithConstraint(ctx context.Context, entities []T, constraint scoped.WriteConstraint) error {
	if len(entities) == 0 {
		return nil
	}
	split, err := constraint.SplitByTargets(r.batchConstraintTargetsForEntities(ctx, entities))
	if err != nil {
		return err
	}
	return r.withConstrainedBatchTx(ctx, func(txCtx context.Context) error {
		items := make([]any, 0, len(entities))
		for i, entity := range entities {
			// 每条实体按各自被授权的范围盖戳，因此准备阶段仍逐条派生 ctx。
			scopedTxCtx, err := split[i].ScopedContext(txCtx)
			if err != nil {
				return err
			}
			if err := r.prepareCreateWithConstraint(scopedTxCtx, entity, split[i]); err != nil {
				return err
			}
			items = append(items, entity)
		}
		quer, err := r.query(txCtx)
		if err != nil {
			return err
		}
		if err := quer.Create(items...); err != nil {
			return writeError(err, "failed to batch save records")
		}
		return nil
	})
}

// updateAllWithConstraint 在显式写入约束下批量更新实体。
func (r *Repo[T, ID]) updateAllWithConstraint(ctx context.Context, entities []T, constraint scoped.WriteConstraint) error {
	if len(entities) == 0 {
		return nil
	}
	split, err := constraint.SplitByTargets(r.batchConstraintTargetsForEntitiesWithConstraint(ctx, entities, constraint))
	if err != nil {
		return err
	}
	return r.withConstrainedBatchTx(ctx, func(txCtx context.Context) error {
		for i, entity := range entities {
			scopedTxCtx, err := split[i].ScopedContext(txCtx)
			if err != nil {
				return err
			}
			if err := r.UpdateWithConstraint(scopedTxCtx, entity, split[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// deleteAllWithConstraint 在显式写入约束下批量删除实体。
func (r *Repo[T, ID]) deleteAllWithConstraint(ctx context.Context, ids []ID, constraint scoped.WriteConstraint) error {
	if len(ids) == 0 {
		return nil
	}
	split, err := constraint.SplitByTargets(r.batchConstraintTargetsForIDs(ctx, ids, constraint))
	if err != nil {
		return err
	}
	return r.withConstrainedBatchTx(ctx, func(txCtx context.Context) error {
		for i, id := range ids {
			scopedTxCtx, err := split[i].ScopedContext(txCtx)
			if err != nil {
				return err
			}
			if err := r.DeleteWithConstraint(scopedTxCtx, id, split[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repo[T, ID]) withConstrainedBatchTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	return tx.RunTxLifecycle(ctx, r, fn)
}

func (r *Repo[T, ID]) batchConstraintTargetsForEntitiesWithConstraint(ctx context.Context, entities []T, constraint scoped.WriteConstraint) []scoped.Resource {
	targets := r.batchConstraintTargetsForEntities(ctx, entities)
	resourcesByID := batchConstraintResourcesByID(constraint.Resources)
	for i := range targets {
		resource, ok := takeBatchConstraintResourceForID(resourcesByID, targets[i].ID)
		if !ok {
			continue
		}
		targets[i].Kind = resource.Kind
		targets[i].ManagedScopeID = resource.ManagedScopeID
		targets[i].GlobalScope = resource.GlobalScope
		targets[i].TenantID = resource.TenantID
		targets[i].Revision = resource.Revision
	}
	return targets
}

func (r *Repo[T, ID]) batchConstraintTargetsForEntities(ctx context.Context, entities []T) []scoped.Resource {
	targets := make([]scoped.Resource, 0, len(entities))
	for _, entity := range entities {
		target := scoped.Resource{
			Kind:     r.writeResourceKind(),
			ID:       formatResourceID(entity.GetID()),
			Revision: formatBatchConstraintVersion(entity.GetVersion()),
		}
		target = r.fillBatchConstraintScope(ctx, entity, target)
		targets = append(targets, target)
	}
	return targets
}

func (r *Repo[T, ID]) batchConstraintTargetsForIDs(ctx context.Context, ids []ID, constraint scoped.WriteConstraint) []scoped.Resource {
	targets := make([]scoped.Resource, 0, len(ids))
	resourcesByID := batchConstraintResourcesByID(constraint.Resources)
	scope, _ := scoped.DataScopeFromContext(ctx)
	visibleScopeIDs := normalizeVisibleScopeIDs(scope)
	for _, id := range ids {
		target := scoped.Resource{Kind: r.writeResourceKind(), ID: formatResourceID(id)}
		if resource, ok := takeBatchConstraintResourceForID(resourcesByID, target.ID); ok {
			target.Kind = resource.Kind
			target.ManagedScopeID = resource.ManagedScopeID
			target.GlobalScope = resource.GlobalScope
			target.TenantID = resource.TenantID
			target.Revision = resource.Revision
		} else if len(visibleScopeIDs) == 1 {
			target.ManagedScopeID = visibleScopeIDs[0]
		}
		targets = append(targets, target)
	}
	return targets
}

func batchConstraintResourcesByID(resources []scoped.ResourceConstraint) map[string][]scoped.ResourceConstraint {
	byID := make(map[string][]scoped.ResourceConstraint, len(resources))
	for _, resource := range resources {
		if resource.ResourceID == "" {
			continue
		}
		byID[resource.ResourceID] = append(byID[resource.ResourceID], resource)
	}
	return byID
}

func takeBatchConstraintResourceForID(resourcesByID map[string][]scoped.ResourceConstraint, id string) (scoped.ResourceConstraint, bool) {
	if id == "" {
		return scoped.ResourceConstraint{}, false
	}
	resources := resourcesByID[id]
	if len(resources) == 0 {
		return scoped.ResourceConstraint{}, false
	}
	resource := resources[0]
	if len(resources) == 1 {
		delete(resourcesByID, id)
	} else {
		resourcesByID[id] = resources[1:]
	}
	return resource, true
}

func (r *Repo[T, ID]) fillBatchConstraintScope(ctx context.Context, entity T, target scoped.Resource) scoped.Resource {
	schema := r.accessSchema()
	value := reflect.ValueOf(entity)
	if schema.managedScope.hasField() {
		if managedScopeID, ok := readInt64Field(value, schema.managedScope.index); ok {
			target.ManagedScopeID = normalizePositiveID(managedScopeID)
		}
	}
	scope, ok := scoped.DataScopeFromContext(ctx)
	if !ok {
		return target
	}
	if target.ManagedScopeID == 0 {
		visibleScopeIDs := normalizeVisibleScopeIDs(scope)
		if len(visibleScopeIDs) == 1 {
			target.ManagedScopeID = visibleScopeIDs[0]
		}
	}
	return target
}

func formatBatchConstraintVersion(version uint64) string {
	if version == 0 {
		return ""
	}
	return strconv.FormatUint(version, 10)
}
