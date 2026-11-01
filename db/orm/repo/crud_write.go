package repo

import (
	"context"
	"fmt"
	"gochen/domain/spec"
	"time"

	"gochen-runtime/internal/sqlresult"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/domain/audited"
	"gochen/errors"
)

// actorFromContext 优先从上下文读取操作人，缺失时回退到仓储默认 actor。
func (r *Repo[T, ID]) actorFromContext(ctx context.Context) string {
	if v := contextx.Operator(ctx); v != "" {
		return v
	}
	return r.defaultActor
}

// EnsureID 为需要预先派生路径/外键的聚合预分配实体 ID。
func (r *Repo[T, ID]) EnsureID(entity T) error {
	var zeroID ID
	if settable, ok := any(entity).(domain.ISettableID[ID]); ok && entity.GetID() == zeroID {
		if r.idGen == nil {
			return errors.NewCode(errors.InvalidInput, "ID generator not configured (use WithIDGenerator)")
		}
		id, err := r.idGen.Next()
		if err != nil {
			return err
		}
		settable.SetID(id)
	}
	return nil
}

// prepareCreate 在创建前补齐隔离归属、ID 和审计/时间戳字段。
//
// 审计实体的创建归因、初始版本与删除态一律由服务端控制：客户端提供的任何
// 初始值都会被覆写，防止经公开入口伪造 created_by/created_at/version 或
// 直接触发软删除态。可信的历史数据导入应使用专门入口，而不是 Create。
func (r *Repo[T, ID]) prepareCreate(ctx context.Context, entity T) error {
	if err := r.stampIsolation(ctx, entity); err != nil {
		return err
	}
	if err := r.EnsureID(entity); err != nil {
		return err
	}

	if aud, ok := any(entity).(audited.IAuditable); ok {
		by := r.actorFromContext(ctx)
		now := time.Now()
		aud.SetCreatedInfo(by, now)
		aud.SetUpdatedInfo(by, now)
		// SetUpdatedInfo 会推版本；创建态统一归零，与内存实现保持一致。
		if setter, ok := any(entity).(domain.ISettableVersion); ok {
			setter.SetVersion(0)
		}
		if deleter, ok := any(entity).(interface {
			SetDeletedInfo(at *time.Time, by *string)
		}); ok {
			deleter.SetDeletedInfo(nil, nil)
		}
		return nil
	}

	if ts, ok := any(entity).(domain.ITimestamps); ok {
		now := time.Now()
		if ts.GetCreatedAt().IsZero() {
			ts.SetCreatedAt(now)
		}
		ts.SetUpdatedAt(now)
	}

	return nil
}

// prepareUpdate 在更新前补齐隔离归属和最后修改信息。
func (r *Repo[T, ID]) prepareUpdate(ctx context.Context, entity T) error {
	if err := r.stampIsolation(ctx, entity); err != nil {
		return err
	}
	if aud, ok := any(entity).(audited.IAuditable); ok {
		aud.SetUpdatedInfo(r.actorFromContext(ctx), time.Now())
		return nil
	}
	if ts, ok := any(entity).(domain.ITimestamps); ok {
		ts.SetUpdatedAt(time.Now())
	}
	return nil
}

// advanceVersion 推进非审计实体的版本；审计实体的版本由 SetUpdatedInfo 推进。
func (r *Repo[T, ID]) advanceVersion(entity T, expected uint64) {
	if r.auditFields || r.versioningOff {
		return
	}
	if setter, ok := any(entity).(domain.ISettableVersion); ok && setter != nil {
		setter.SetVersion(expected + 1)
	}
}

// restoreEntityVersion 在写入失败后把实体版本恢复到进入更新时的值。
//
// 版本推进发生在写入之前；若写入失败（并发冲突 / 目标不存在 / 基础设施错误）
// 后内存版本停留在已推进值，调用方用同一对象直接重试会"恰好"命中数据库里的
// 新版本并覆盖别人的写入。事务整体回滚同理：内存版本与数据库已不一致，
// 调用方应重新 Get，而不是复用旧对象。
func restoreEntityVersion[T domain.IEntity[ID], ID comparable](entity T, version uint64) {
	if setter, ok := any(entity).(domain.ISettableVersion); ok && setter != nil {
		setter.SetVersion(version)
	}
}

func rowsAffected(result interface{ RowsAffected() (int64, error) }, action string) (int64, error) {
	return sqlresult.ReadRowsAffected(result, action+" did not return a result", "failed to read "+action+" result")
}

// requireWriteSpec 校验写路径规约：必须是纯谓词，且 Create 不得引用 RoleID。
func requireWriteSpec(s spec.Spec, creating bool) error {
	if err := s.Err(); err != nil {
		return errors.Wrap(err, errors.Internal, "invalid spec")
	}
	if s.HasNamed() {
		return errors.NewCode(errors.Internal, "write spec must not contain named conditions")
	}
	if creating && s.ReferencesRole(spec.RoleID) {
		return errors.NewCode(errors.Internal, "create spec must not reference the id role")
	}
	return nil
}

// checkWriteSpec 用内存解释器对实体做写入前 WITH CHECK。
func (r *Repo[T, ID]) checkWriteSpec(s spec.Spec, entity T) error {
	ok, err := spec.NewInterpreter().Match(s, entity)
	if err != nil {
		return err
	}
	if !ok {
		return errors.NewCode(errors.Forbidden, "entity does not satisfy the required scope").
			WithContext("id", fmt.Sprint(entity.GetID())).
			WithContext("spec", s.Canonical())
	}
	return nil
}

func (r *Repo[T, ID]) versioned() bool {
	return r.access.version != "" && !r.versioningOff
}

// immutableColumns 返回永不出现在 UPDATE SET 中的列：
// 主键与访问控制角色列。隔离列即使没有角色标签也按访问控制列处理。
func (r *Repo[T, ID]) immutableColumns() map[string]struct{} {
	columns := []string{r.idColumn, r.access.tenant, r.access.scope, r.access.owner, r.access.isolation}
	excluded := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if column != "" {
			excluded[column] = struct{}{}
		}
	}
	return excluded
}

func (r *Repo[T, ID]) updateValues(entity T) (map[string]any, error) {
	return r.resolver.updateValues(entity, r.immutableColumns())
}

func notFoundError[ID comparable](id ID) error {
	return errors.NewCode(errors.NotFound, "record not found").WithContext("id", id)
}

func concurrencyError[ID comparable](id ID, expected uint64) error {
	return errors.NewCode(errors.Concurrency, "concurrent modification detected").
		WithContext("id", id).
		WithContext("expected_version", expected)
}

// probeExists 在同一事务内按 id + 规约探查记录是否存在。
//
// 探查复用写路径的严格隔离条件，不会泄露调用方无权看到的记录是否存在。
func (r *Repo[T, ID]) probeExists(ctx context.Context, id ID, condition orm.Condition, includeDeleted bool) (bool, error) {
	quer, err := r.query(ctx)
	if err != nil {
		return false, err
	}
	quer = r.withID(quer, id)
	if !includeDeleted {
		quer = r.withActive(quer)
	}
	quer = applyCondition(quer, condition)
	count, err := quer.Count()
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "failed to probe record")
	}
	return count > 0, nil
}

// classifyWriteMiss 把“写入命中 0 行”分为 NotFound 与 Concurrency。
func (r *Repo[T, ID]) classifyWriteMiss(ctx context.Context, id ID, condition orm.Condition, expectedVersion uint64) error {
	found, err := r.probeExists(ctx, id, condition, false)
	if err != nil {
		return err
	}
	if found && r.versioned() {
		return concurrencyError(id, expectedVersion)
	}
	return notFoundError(id)
}

// classifyRestoreMiss 把“恢复命中 0 行”分为 NotFound、Conflict 与 Concurrency。
func (r *Repo[T, ID]) classifyRestoreMiss(ctx context.Context, id ID, condition orm.Condition, expectedVersion uint64) error {
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = r.withID(quer, id)
	quer = applyCondition(quer, condition)
	var existing T
	if err := quer.First(&existing); err != nil {
		if errors.Is(err, errors.NotFound) {
			return notFoundError(id)
		}
		return errors.Wrap(err, errors.Database, "failed to probe restore target")
	}
	deletable, ok := any(existing).(domain.ISoftDeletable)
	if !ok {
		return errors.NewCode(errors.Internal, "entity does not implement domain.ISoftDeletable")
	}
	if !deletable.IsDeleted() {
		return errors.NewCode(errors.Conflict, "entity is not deleted").WithContext("id", id)
	}
	return concurrencyError(id, expectedVersion)
}

// Create 创建并持久化一条实体记录。
func (r *Repo[T, ID]) Create(ctx context.Context, entity T, s spec.Spec) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, true); err != nil {
		return err
	}
	if err := r.prepareCreate(ctx, entity); err != nil {
		return err
	}
	if err := r.checkWriteSpec(s, entity); err != nil {
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

// Update 按主键更新实体。
//
// 语义契约（见 domain/crud.IRepository）：
//   - 版本化实体默认开启乐观锁：更新以 WHERE id ∧ 规约 ∧ version 原子执行，
//     版本随更新推进；未命中时区分 NotFound（不存在/不可见/已软删）与
//     Concurrency（版本不匹配）；
//   - 访问控制列（tenant/scope/owner 与隔离列）不进入 SET；
//   - 更新失败（含并发冲突）时实体版本保持进入时的值。
func (r *Repo[T, ID]) Update(ctx context.Context, entity T, s spec.Spec) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	condition, err := r.writeSpecCondition(s)
	if err != nil {
		return err
	}
	expectedVersion := entity.GetVersion()
	if err := r.prepareUpdate(ctx, entity); err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	r.advanceVersion(entity, expectedVersion)
	if err := r.checkWriteSpec(s, entity); err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}

	quer, err := r.query(ctx)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	quer = r.withID(quer, entity.GetID())
	quer = r.withActive(quer)
	if r.versioned() {
		quer = quer.Where(r.resolver.quote(r.access.version)+" = ?", expectedVersion)
	}
	quer = applyCondition(quer, condition)

	values, err := r.updateValues(entity)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	result, err := quer.UpdateValuesWithResult(values)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return writeError(err, "failed to update record")
	}
	affected, err := rowsAffected(result, "update")
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	if affected == 0 {
		restoreEntityVersion(entity, expectedVersion)
		return r.classifyWriteMiss(ctx, entity.GetID(), condition, expectedVersion)
	}
	return nil
}

// Delete 按主键物理删除实体（包括按软删除列判断仍处于活动态的行）。
func (r *Repo[T, ID]) Delete(ctx context.Context, entity T, s spec.Spec) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	condition, err := r.writeSpecCondition(s)
	if err != nil {
		return err
	}
	expectedVersion := entity.GetVersion()

	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = r.withID(quer, entity.GetID())
	quer = r.withActive(quer)
	if r.versioned() {
		quer = quer.Where(r.resolver.quote(r.access.version)+" = ?", expectedVersion)
	}
	quer = applyCondition(quer, condition)

	result, err := quer.DeleteWithResult()
	if err != nil {
		return errors.Wrap(err, errors.Database, "failed to delete record")
	}
	affected, err := rowsAffected(result, "delete")
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.classifyWriteMiss(ctx, entity.GetID(), condition, expectedVersion)
	}
	return nil
}

// Purge 对指定记录执行物理删除，包括已软删的行。
func (r *Repo[T, ID]) Purge(ctx context.Context, entity T, s spec.Spec) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	condition, err := r.writeSpecCondition(s)
	if err != nil {
		return err
	}
	expectedVersion := entity.GetVersion()

	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = r.withID(quer, entity.GetID())
	if r.versioned() {
		quer = quer.Where(r.resolver.quote(r.access.version)+" = ?", expectedVersion)
	}
	quer = applyCondition(quer, condition)

	result, err := quer.DeleteWithResult()
	if err != nil {
		return errors.Wrap(err, errors.Database, "failed to permanently delete record")
	}
	affected, err := rowsAffected(result, "purge")
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.classifyWriteMiss(ctx, entity.GetID(), condition, expectedVersion)
	}
	return nil
}

// Restore 恢复已软删实体；要求当前行仍处于删除态，作为原子条件。
func (r *Repo[T, ID]) Restore(ctx context.Context, entity T, s spec.Spec) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if !r.softDelete {
		return errors.NewCode(errors.Unsupported, "restore requires a soft-deletable entity")
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	condition, err := r.writeSpecCondition(s)
	if err != nil {
		return err
	}
	expectedVersion := entity.GetVersion()
	if err := r.prepareUpdate(ctx, entity); err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	r.advanceVersion(entity, expectedVersion)
	if err := r.checkWriteSpec(s, entity); err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}

	quer, err := r.query(ctx)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	quer = r.withID(quer, entity.GetID())
	quer = r.withDeleted(quer)
	if r.versioned() {
		quer = quer.Where(r.resolver.quote(r.access.version)+" = ?", expectedVersion)
	}
	quer = applyCondition(quer, condition)

	values, err := r.updateValues(entity)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	result, err := quer.UpdateValuesWithResult(values)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return writeError(err, "failed to restore record")
	}
	affected, err := rowsAffected(result, "restore")
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	if affected == 0 {
		restoreEntityVersion(entity, expectedVersion)
		return r.classifyRestoreMiss(ctx, entity.GetID(), condition, expectedVersion)
	}
	return nil
}

// CreateBulk 在同一事务里批量创建多条实体记录。
func (r *Repo[T, ID]) CreateBulk(ctx context.Context, entities []T, s spec.Spec) error {
	if len(entities) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, true); err != nil {
		return err
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		items := make([]any, 0, len(entities))
		for i := range entities {
			if err := r.prepareCreate(txCtx, entities[i]); err != nil {
				return err
			}
			if err := r.checkWriteSpec(s, entities[i]); err != nil {
				return err
			}
			items = append(items, entities[i])
		}
		quer, err := r.query(txCtx)
		if err != nil {
			return err
		}
		if err := quer.Create(items...); err != nil {
			return writeError(err, "failed to bulk save records")
		}
		return nil
	})
}

// UpdateBulk 在同一事务里逐条更新实体；任一实体失败整批回滚。
func (r *Repo[T, ID]) UpdateBulk(ctx context.Context, entities []T, s spec.Spec) error {
	if len(entities) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		for i := range entities {
			if err := r.Update(txCtx, entities[i], s); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteBulk 在同一事务里逐条物理删除实体；任一实体失败整批回滚。
func (r *Repo[T, ID]) DeleteBulk(ctx context.Context, entities []T, s spec.Spec) error {
	if len(entities) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	if err := requireWriteSpec(s, false); err != nil {
		return err
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		for i := range entities {
			if err := r.Delete(txCtx, entities[i], s); err != nil {
				return err
			}
		}
		return nil
	})
}
