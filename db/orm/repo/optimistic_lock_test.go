package repo

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"
)

type fakeResult int64

type modelWithoutResult struct {
	orm.IModel
	saveCalls int
}

func (m *modelWithoutResult) Save(context.Context, any, ...orm.QueryOption) error {
	m.saveCalls++
	return nil
}

func TestRepo_Update_DoesNotSilentlyDisableOptimisticLock(t *testing.T) {
	m := &modelWithoutResult{IModel: &optimisticModel{}}
	r := &Repo[*auditedEntity, int64]{model: m, auditFields: true, defaultActor: "system"}
	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	err := r.Update(context.Background(), e)
	if !errors.Is(err, errors.Unsupported) || errors.Code(err) != errors.Unsupported || m.saveCalls != 0 {
		t.Fatalf("optimistic lock was bypassed: err=%v saveCalls=%d", err, m.saveCalls)
	}
}

// LastInsertId result1：数值结果。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }

// RowsAffected result1：数值结果。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type optimisticModel struct {
	affected int64
	count    int64
}

// Meta result：返回的实例（类型：*orm.ModelMeta）。
//
// 返回：
func (m *optimisticModel) Meta() *orm.ModelMeta { return &orm.ModelMeta{Table: "t"} }

// Capabilities result：测试返回值（类型：orm.Capabilities）。
//
// 返回：
func (m *optimisticModel) Capabilities() orm.Capabilities { return orm.Capabilities{} }

func (m *optimisticModel) Dialect() repoTestDialect { return unknownRepoTestDialect() }

// First context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) First(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}

// Find 从存储中查询对象。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) Find(context.Context, any, ...orm.QueryOption) error { panic("not used") }

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return m.count, nil
}

// Create 创建对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：可变参数（类型：...any）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) Create(context.Context, ...any) error { panic("not used") }

// Save context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) Save(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}

// UpdateValues 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - attrs：属性/参数集合（类型：map[string]any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	panic("not used")
}

// Delete 删除对象并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) Delete(context.Context, ...orm.QueryOption) error { panic("not used") }

// Association 返回关联查询对象（测试 stub）。
//
// 参数：
// - owner：关联主对象
// - name：关联名
//
// 返回：
// - result：关联查询对象
func (m *optimisticModel) Association(owner any, name string) orm.IAssociation {
	_ = owner
	_ = name
	return nil
}

// SaveWithResult context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：返回结果（类型：sql.Result）
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) SaveWithResult(context.Context, any, ...orm.QueryOption) (sql.Result, error) {
	return fakeResult(m.affected), nil
}

// UpdateValuesWithResult 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - attrs：属性/参数集合（类型：map[string]any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：返回结果（类型：sql.Result）
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) UpdateValuesWithResult(context.Context, map[string]any, ...orm.QueryOption) (sql.Result, error) {
	panic("not used")
}

// DeleteWithResult 删除对象并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：返回结果（类型：sql.Result）
// - err：错误信息（nil 表示成功）
func (m *optimisticModel) DeleteWithResult(context.Context, ...orm.QueryOption) (sql.Result, error) {
	panic("not used")
}

// TestRepo_Update_OptimisticLockReturnsNotFoundWhenZeroAffectedAndNoExists 验证 Repo Update OptimisticLockReturnsNotFoundWhenZeroAffectedAndNoExists。
func TestRepo_Update_OptimisticLockReturnsNotFoundWhenZeroAffectedAndNoExists(t *testing.T) {
	m := &optimisticModel{affected: 0, count: 0}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     false,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	if err := r.Update(context.Background(), e); !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// TestRepo_Update_OptimisticLockReturnsConcurrencyWhenZeroAffectedButExists 验证 Repo Update OptimisticLockReturnsConcurrencyWhenZeroAffectedButExists。
func TestRepo_Update_OptimisticLockReturnsConcurrencyWhenZeroAffectedButExists(t *testing.T) {
	m := &optimisticModel{affected: 0, count: 1}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     false,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	err := r.Update(context.Background(), e)
	if !errors.Is(err, errors.Concurrency) {
		t.Fatalf("expected Concurrency, got %v", err)
	}
	var appErr *errors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		if got := appErr.Details()["id"]; got != int64(1) {
			t.Fatalf("expected id=1, got %v", got)
		}
		if got := appErr.Details()["expected_version"]; got != uint64(7) {
			t.Fatalf("expected expected_version=7, got %v", got)
		}
	}
}

type auditedEntity struct {
	*audited.AuditedEntity[int64]
}

type capturingOptimisticModel struct {
	optimisticModel
	saveOpts  orm.QueryOptions
	countOpts orm.QueryOptions
}

// Count 返回匹配条件的记录数。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - opts：可选参数/配置项
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (m *capturingOptimisticModel) Count(ctx context.Context, opts ...orm.QueryOption) (int64, error) {
	_ = ctx
	m.countOpts = orm.CollectQueryOptions(opts...)
	return m.optimisticModel.count, nil
}

// SaveWithResult ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - entity：实体对象
// - opts：可选参数/配置项
//
// 返回：
// - result1：返回结果（类型：sql.Result）
// - err：错误信息（nil 表示成功）
func (m *capturingOptimisticModel) SaveWithResult(ctx context.Context, entity any, opts ...orm.QueryOption) (sql.Result, error) {
	_ = ctx
	_ = entity
	m.saveOpts = orm.CollectQueryOptions(opts...)
	return fakeResult(m.optimisticModel.affected), nil
}

// hasWhereExpr opts：可选参数/配置项。
//
// 参数：
// - contains：参数值（具体语义见函数上下文）（类型：string）
//
// 返回：
// - result：是否满足条件
func hasWhereExpr(opts orm.QueryOptions, contains string) bool {
	for _, c := range opts.Where {
		if strings.Contains(c.Expr, contains) {
			return true
		}
	}
	return false
}

// TestRepo_Update_FiltersSoftDeletedInWhere 验证 Repo Update FIltersSoftDeletedInWhere。
func TestRepo_Update_FiltersSoftDeletedInWhere(t *testing.T) {
	m := &capturingOptimisticModel{optimisticModel: optimisticModel{affected: 1, count: 1}}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	if err := r.Update(context.Background(), e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasWhereExpr(m.saveOpts, "deleted_at") {
		t.Fatalf("expected Update where clause to include deleted_at filter, got: %+v", m.saveOpts.Where)
	}
}

// TestRepo_Update_ExistsCheckFiltersSoftDeleted 验证 Repo Update ExistsCheckFiltersSoftDeleted。
func TestRepo_Update_ExistsCheckFiltersSoftDeleted(t *testing.T) {
	m := &capturingOptimisticModel{optimisticModel: optimisticModel{affected: 0, count: 1}}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	if err := r.Update(context.Background(), e); !errors.Is(err, errors.Concurrency) {
		t.Fatalf("expected Concurrency, got %v", err)
	}
	if !hasWhereExpr(m.countOpts, "deleted_at") {
		t.Fatalf("expected exists check to include deleted_at filter, got: %+v", m.countOpts.Where)
	}
}

// TestRepo_Update_SoftDeletedRecordReturnsNotFound 验证软删除记录不再被 Update 视为可写记录。
func TestRepo_Update_SoftDeletedRecordReturnsNotFound(t *testing.T) {
	m := &capturingOptimisticModel{optimisticModel: optimisticModel{affected: 0, count: 0}}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	e := &auditedEntity{AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 7}}}
	if err := r.Update(context.Background(), e); !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
