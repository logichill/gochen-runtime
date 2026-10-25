package repo

import (
	"context"
	"time"

	"gochen-runtime/internal/sqlresult"
	"gochen/contextx"
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

// prepareCreate 在创建前补齐数据域、ID 和审计/时间戳字段。
//
// 审计实体的创建归因、初始版本与删除态一律由服务端控制：客户端提供的任何
// 初始值都会被覆写，防止经公开入口伪造 created_by/created_at/version 或
// 直接触发软删除态。可信的历史数据导入应使用专门入口，而不是 Create。
func (r *Repo[T, ID]) prepareCreate(ctx context.Context, entity T) error {
	if err := r.applyDataScopeToEntity(ctx, entity); err != nil {
		return err
	}
	if err := r.EnsureID(entity); err != nil {
		return err
	}

	if aud, ok := any(entity).(audited.IAuditable); ok {
		by := r.actorFromContext(ctx)
		now := time.Now()
		if setter, ok := any(entity).(domain.ISettableVersion); ok {
			setter.SetVersion(0)
		}
		aud.SetCreatedInfo(by, now)
		aud.SetUpdatedInfo(by, now)
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

// prepareUpdate 在更新前补齐数据域和最后修改信息。
func (r *Repo[T, ID]) prepareUpdate(ctx context.Context, entity T) error {
	if err := r.applyDataScopeToEntity(ctx, entity); err != nil {
		return err
	}
	if aud, ok := any(entity).(audited.IAuditable); ok {
		by := r.actorFromContext(ctx)
		aud.SetUpdatedInfo(by, time.Now())
		return nil
	}

	if ts, ok := any(entity).(domain.ITimestamps); ok {
		ts.SetUpdatedAt(time.Now())
	}
	return nil
}

// activeRecordExists 检查记录是否仍是当前仓储可写的 active 记录。
func (r *Repo[T, ID]) activeRecordExists(ctx context.Context, id ID) (bool, error) {
	return r.recordExists(ctx, id, true)
}

// recordExists 检查记录是否仍在当前仓储可见范围内。
func (r *Repo[T, ID]) recordExists(ctx context.Context, id ID, activeOnly bool) (bool, error) {
	quer, err := r.query(ctx)
	if err != nil {
		return false, err
	}
	quer = quer.Where("id = ?", id)
	if activeOnly && r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	count, err := quer.Count()
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "failed to check record existence")
	}
	return count > 0, nil
}

func rowsAffected(result interface{ RowsAffected() (int64, error) }, action string) (int64, error) {
	affected, err := sqlresult.ReadRowsAffected(result, action+" did not return a result", "failed to read "+action+" result")
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// fallbackOnUnsupported 在 result 不被模型支持时执行回退动作。
//
// 用于删除类路径：先确认记录仍存在（避免把"不存在"误报为回退成功），
// 再执行不带 RowsAffected 的等价操作。回退动作本身失败时返回 Database 错误。
func (r *Repo[T, ID]) fallbackOnUnsupported(ctx context.Context, id ID, activeOnly bool, action string, fallback func() error) error {
	exists, existsErr := r.recordExists(ctx, id, activeOnly)
	if existsErr != nil {
		return existsErr
	}
	if !exists {
		return errors.NewCode(errors.NotFound, "record not found").WithContext("id", id)
	}
	if err := fallback(); err != nil {
		return errors.Wrap(err, errors.Database, action)
	}
	return nil
}

func requireRowsAffected(result interface{ RowsAffected() (int64, error) }, action string, id any) error {
	affected, err := rowsAffected(result, action)
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.NewCode(errors.NotFound, "record not found").WithContext("id", id)
	}
	return nil
}

// classifyActiveUpdateMiss 把“乐观锁更新命中 0 行”细分为：
// - 记录已不存在/已软删：返回 NotFound；
// - 记录仍存在但版本不匹配：返回 Concurrency。
func (r *Repo[T, ID]) classifyActiveUpdateMiss(ctx context.Context, id ID, expectedVersion uint64) error {
	exists, err := r.activeRecordExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return errors.NewCode(errors.NotFound, "record not found").WithContext("id", id)
	}
	return errors.NewCode(errors.Concurrency, "concurrent modification detected").
		WithContext("id", id).
		WithContext("expected_version", expectedVersion)
}

// Create 创建并持久化一条实体记录。
//
// 声明了范围列（WithScope）的仓储必须先通过受控约束通道取得写约束，
// 取不到即 fail-closed 拒绝（见 constraint_channel.go）。
func (r *Repo[T, ID]) Create(ctx context.Context, entity T) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.CreateWithConstraint(ctx, entity, constraint)
	}
	if err := r.prepareCreate(ctx, entity); err != nil {
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

// restoreEntityVersion 在写入失败后把实体版本恢复到进入更新时的值。
//
// 版本推进发生在写入之前（prepareUpdate 的 SetUpdatedInfo 或仓储的 SetVersion）；
// 若写入失败（并发冲突 / 目标不存在 / 基础设施错误）后内存版本停留在已推进值，
// 调用方用同一对象直接重试会"恰好"命中数据库里的新版本并覆盖别人的写入。
// 事务整体回滚同理：内存版本与数据库已不一致，调用方应重新 Get，而不是复用旧对象。
func restoreEntityVersion[T domain.IEntity[ID], ID comparable](entity T, version uint64) {
	if setter, ok := any(entity).(domain.ISettableVersion); ok && setter != nil {
		setter.SetVersion(version)
	}
}

// Update 按主键更新实体。
//
// 语义契约（见 domain/crud.IRepository）：
//   - 版本化实体（嵌入 crud.Entity / AuditedEntity，或显式启用审计）默认开启
//     乐观锁：更新以 WHERE id = ? AND version = ? 原子执行，同一版本只有一次
//     更新成功，版本随更新推进；未命中时区分 NotFound（记录不存在或已软删）
//     与 Concurrency（版本不匹配）；
//   - 非版本化实体不施加版本条件，但同样检查影响行数：目标不存在返回 NotFound，
//     不再静默成功；
//   - 更新失败（含并发冲突）时实体版本保持进入时的值，重试同一对象不会
//     意外命中新版本。
func (r *Repo[T, ID]) Update(ctx context.Context, entity T) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.UpdateWithConstraint(ctx, entity, constraint)
	}
	expectedVersion := entity.GetVersion()
	if err := r.prepareUpdate(ctx, entity); err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	quer, err := r.query(ctx)
	if err != nil {
		restoreEntityVersion(entity, expectedVersion)
		return err
	}
	quer = quer.Where("id = ?", entity.GetID())
	if r.softDelete {
		quer = quer.Where(r.softDeleteCols.DeletedAt + " IS NULL")
	}
	if r.versioned() {
		// 版本化实体：非审计路径的版本推进由仓储完成；审计路径在 prepareUpdate
		// 中随 SetUpdatedInfo 推进，此处不重复推进。
		if !r.auditFields {
			if setter, ok := any(entity).(domain.ISettableVersion); ok && setter != nil {
				setter.SetVersion(expectedVersion + 1)
			}
		}
		quer = quer.Where(r.versionColumn()+" = ?", expectedVersion)
		res, err := quer.SaveWithResult(entity)
		if err != nil {
			restoreEntityVersion(entity, expectedVersion)
			if errors.Is(err, errors.Unsupported) {
				return errors.Wrap(err, errors.Unsupported, "optimistic locking requires an orm model with result support")
			}
			return writeError(err, "failed to update record")
		}
		affected, err := rowsAffected(res, "update")
		if err != nil {
			restoreEntityVersion(entity, expectedVersion)
			return err
		}
		if affected == 0 {
			restoreEntityVersion(entity, expectedVersion)
			return r.classifyActiveUpdateMiss(ctx, entity.GetID(), expectedVersion)
		}
		return nil
	}
	res, err := quer.SaveWithResult(entity)
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return r.fallbackOnUnsupported(ctx, entity.GetID(), true, "failed to update record", func() error {
				return quer.Save(entity)
			})
		}
		return writeError(err, "failed to update record")
	}
	affected, err := rowsAffected(res, "update")
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.NewCode(errors.NotFound, "record not found").WithContext("id", entity.GetID())
	}
	return nil
}

// Delete 删除指定记录；启用软删除时会改写删除标记而非物理删除。
func (r *Repo[T, ID]) Delete(ctx context.Context, id ID) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.DeleteWithConstraint(ctx, id, constraint)
	}
	if r.softDelete {
		values, err := r.softDeleteValues(ctx)
		if err != nil {
			return err
		}

		quer, err := r.query(ctx)
		if err != nil {
			return err
		}
		quer = quer.Where("id = ?", id).Where(r.softDeleteCols.DeletedAt + " IS NULL")
		result, err := quer.UpdateValuesWithResult(values)
		if err != nil {
			if errors.Is(err, errors.Unsupported) {
				return r.fallbackOnUnsupported(ctx, id, true, "failed to delete record", func() error {
					return quer.UpdateValues(values)
				})
			}
			return errors.Wrap(err, errors.Database, "failed to delete record")
		}
		return requireRowsAffected(result, "delete", id)
	}

	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = quer.Where("id = ?", id)
	result, err := quer.DeleteWithResult()
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return r.fallbackOnUnsupported(ctx, id, true, "failed to delete record", func() error {
				return quer.Delete()
			})
		}
		return errors.Wrap(err, errors.Database, "failed to delete record")
	}
	return requireRowsAffected(result, "delete", id)
}

// Purge 对指定记录执行物理删除，不保留软删除痕迹。
func (r *Repo[T, ID]) Purge(ctx context.Context, id ID) error {
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, gateErr := r.constraintGate(ctx)
	if gateErr != nil {
		return gateErr
	}
	if constrained {
		return r.purgeWithConstraint(ctx, id, constraint)
	}
	quer, err := r.query(ctx)
	if err != nil {
		return err
	}
	quer = quer.Where("id = ?", id)
	result, err := quer.DeleteWithResult()
	if err != nil {
		if errors.Is(err, errors.Unsupported) {
			return r.fallbackOnUnsupported(ctx, id, false, "failed to permanently delete record", func() error {
				return quer.Delete()
			})
		}
		return errors.Wrap(err, errors.Database, "failed to permanently delete record")
	}
	return requireRowsAffected(result, "purge", id)
}

// CreateAll 在同一事务里批量创建多条实体记录。
func (r *Repo[T, ID]) CreateAll(ctx context.Context, entities []T) error {
	if len(entities) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.createAllWithConstraint(ctx, entities, constraint)
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		items := make([]any, 0, len(entities))
		for i := range entities {
			if err := r.prepareCreate(txCtx, entities[i]); err != nil {
				return err
			}
			items = append(items, entities[i])
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

// UpdateAll 在同一事务里逐条更新实体。
func (r *Repo[T, ID]) UpdateAll(ctx context.Context, entities []T) error {
	if len(entities) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.updateAllWithConstraint(ctx, entities, constraint)
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		for i := range entities {
			if err := r.Update(txCtx, entities[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteAll 在同一事务里批量删除指定 ID 对应的记录。
//
// 批量删除采用幂等语义：不存在或已软删的 ID 不会导致 NotFound；单条 Delete 保持严格命中语义。
//
// 例外：声明了范围列的仓储走约束删除路径时逐条下推原子约束，未命中的 ID 会如实
// 报 NotFound / Conflict 并整批回滚——L3 下"删了几条"必须与授权清单严格对齐，
// 静默跳过等于让调用方以为整批都被授权且生效了。
func (r *Repo[T, ID]) DeleteAll(ctx context.Context, ids []ID) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.guardCrossIsolationWrite(ctx); err != nil {
		return err
	}
	constraint, constrained, err := r.constraintGate(ctx)
	if err != nil {
		return err
	}
	if constrained {
		return r.deleteAllWithConstraint(ctx, ids, constraint)
	}
	return r.WithinTx(ctx, func(txCtx context.Context) error {
		// ID 数量可能达到批量上限，按占位参数预算分片下发，避免撞驱动的
		// 占位参数上限。分片全在本事务内，原子性与单条语句时一致。
		base, err := r.query(txCtx)
		if err != nil {
			return err
		}
		if r.softDelete {
			values, err := r.softDeleteValues(txCtx)
			if err != nil {
				return err
			}
			budget, err := r.idBatchBudget(base, len(values))
			if err != nil {
				return err
			}
			for _, chunk := range chunkSlice(ids, budget) {
				quer := base.clone()
				quer = quer.Where("id IN ?", chunk).Where(r.softDeleteCols.DeletedAt + " IS NULL")
				result, err := quer.UpdateValuesWithResult(values)
				if err != nil {
					if errors.Is(err, errors.Unsupported) {
						if err := quer.UpdateValues(values); err != nil {
							return errors.Wrap(err, errors.Database, "failed to batch delete records")
						}
						continue
					}
					return errors.Wrap(err, errors.Database, "failed to batch delete records")
				}
				if _, err := rowsAffected(result, "batch delete"); err != nil {
					return err
				}
			}
			return nil
		}

		budget, err := r.idBatchBudget(base, 0)
		if err != nil {
			return err
		}
		for _, chunk := range chunkSlice(ids, budget) {
			quer := base.clone()
			quer = quer.Where("id IN ?", chunk)
			result, err := quer.DeleteWithResult()
			if err != nil {
				if errors.Is(err, errors.Unsupported) {
					if err := quer.Delete(); err != nil {
						return errors.Wrap(err, errors.Database, "failed to batch delete records")
					}
					continue
				}
				return errors.Wrap(err, errors.Database, "failed to batch delete records")
			}
			if _, err := rowsAffected(result, "batch delete"); err != nil {
				return err
			}
		}
		return nil
	})
}
