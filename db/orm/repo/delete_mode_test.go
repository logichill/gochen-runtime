package repo

import (
	"context"
	"database/sql"
	"testing"

	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
)

type deleteModeModel struct {
	deleted           bool
	updateCalled      bool
	updateAffected    int64
	deleteAffected    int64
	count             int64
	lastUpdateValues  map[string]any
	lastUpdateOptions orm.QueryOptions
	lastDeleteOptions orm.QueryOptions
}

// Meta result：返回的实例（类型：*orm.ModelMeta）。
//
// 返回：
func (m *deleteModeModel) Meta() *orm.ModelMeta { return &orm.ModelMeta{Table: "t"} }

// Capabilities result：测试返回值（类型：orm.Capabilities）。
//
// 返回：
func (m *deleteModeModel) Capabilities() orm.Capabilities { return orm.Capabilities{} }

func (m *deleteModeModel) Dialect() repoTestDialect { return unknownRepoTestDialect() }

// First context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *deleteModeModel) First(context.Context, any, ...orm.QueryOption) error {
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
func (m *deleteModeModel) Find(context.Context, any, ...orm.QueryOption) error { panic("not used") }

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (m *deleteModeModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
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
func (m *deleteModeModel) Create(context.Context, ...any) error { panic("not used") }

// Save context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *deleteModeModel) Save(context.Context, any, ...orm.QueryOption) error { panic("not used") }

// UpdateValues 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - attrs：属性/参数集合（类型：map[string]any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *deleteModeModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	m.updateCalled = true
	return nil
}

func (m *deleteModeModel) UpdateValuesWithResult(ctx context.Context, attrs map[string]any, opts ...orm.QueryOption) (sql.Result, error) {
	_ = ctx
	m.updateCalled = true
	m.lastUpdateValues = attrs
	m.lastUpdateOptions = orm.CollectQueryOptions(opts...)
	return fakeResult(m.updateAffected), nil
}

// Delete 删除对象并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *deleteModeModel) Delete(context.Context, ...orm.QueryOption) error {
	m.deleted = true
	return nil
}

func (m *deleteModeModel) SaveWithResult(context.Context, any, ...orm.QueryOption) (sql.Result, error) {
	panic("not used")
}

func (m *deleteModeModel) DeleteWithResult(context.Context, ...orm.QueryOption) (sql.Result, error) {
	m.deleted = true
	return fakeResult(m.deleteAffected), nil
}

// Association 返回关联查询对象（测试 stub）。
//
// 参数：
// - owner：关联主对象
// - name：关联名
//
// 返回：
// - result：关联查询对象
func (m *deleteModeModel) Association(owner any, name string) orm.IAssociation {
	_ = owner
	_ = name
	return nil
}

// TestRepo_Purge_AlwaysHardDeletes 验证 Repo Purge AlwaysHardDeletes。
func TestRepo_Purge_AlwaysHardDeletes(t *testing.T) {
	m := &deleteModeModel{deleteAffected: 1}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    false,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	if err := r.Purge(context.Background(), int64(1)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.deleted {
		t.Fatalf("expected permanent delete to call model.DeleteWithResult")
	}
	if m.updateCalled {
		t.Fatalf("expected permanent delete to not call model.UpdateValues")
	}
}

func TestRepo_Purge_ReturnsNotFoundWhenNoRowsAffected(t *testing.T) {
	m := &deleteModeModel{deleteAffected: 0}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    false,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	err := r.Purge(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if !m.deleted {
		t.Fatalf("expected permanent delete to call model.DeleteWithResult")
	}
}

func TestRepo_Delete_SoftDeleteReturnsNotFoundWhenRowIsMissing(t *testing.T) {
	m := &deleteModeModel{updateAffected: 0}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	err := r.Delete(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if !hasWhereExpr(m.lastUpdateOptions, "deleted_at") {
		t.Fatalf("expected soft delete to filter active rows, got: %+v", m.lastUpdateOptions.Where)
	}
	if _, ok := m.lastUpdateValues["deleted_at"]; !ok {
		t.Fatalf("expected deleted_at to be set, got: %+v", m.lastUpdateValues)
	}
}

func TestRepo_Delete_HardDeleteReturnsNotFoundWhenRowIsMissing(t *testing.T) {
	m := &deleteModeModel{deleteAffected: 0}
	r := &Repo[*auditedEntity, int64]{
		model:        m,
		auditFields:  false,
		defaultActor: "system",
	}

	err := r.Delete(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if !m.deleted {
		t.Fatalf("expected hard delete to call model.DeleteWithResult")
	}
}

func TestRepo_DeleteAll_SoftDeleteUsesCurrentDeletableRows(t *testing.T) {
	m := &deleteModeModel{updateAffected: 1, count: 1}
	session := &deleteModeSession{model: m}
	r := &Repo[*auditedEntity, int64]{
		orm:            session,
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	err := r.DeleteAll(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("expected idempotent success for already deleted/missing rows, got %v", err)
	}
	if !hasWhereExpr(m.lastUpdateOptions, "deleted_at") {
		t.Fatalf("expected batch soft delete to filter active rows, got: %+v", m.lastUpdateOptions.Where)
	}
}

// TestRepo_DeleteAll_DeduplicatesExpectedRows 验证重复 ID 不会让期望行数虚高而误报 NotFound。
func TestRepo_DeleteAll_DeduplicatesExpectedRows(t *testing.T) {
	m := &deleteModeModel{updateAffected: 2, count: 2}
	session := &deleteModeSession{model: m}
	r := &Repo[*auditedEntity, int64]{
		orm:            session,
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	if err := r.DeleteAll(context.Background(), []int64{1, 1, 2}); err != nil {
		t.Fatalf("expected success for duplicate ids matching distinct count, got %v", err)
	}
}

func TestRepo_DeleteFallbacksToPlainModelWhenResultUnsupported(t *testing.T) {
	m := &deleteFallbackModel{count: 1}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	if err := r.Delete(context.Background(), int64(1)); err != nil {
		t.Fatalf("expected soft delete fallback to succeed, got %v", err)
	}
	if !m.updateCalled {
		t.Fatalf("expected soft delete fallback to call UpdateValues")
	}

	m = &deleteFallbackModel{count: 1}
	r = &Repo[*auditedEntity, int64]{
		model:        m,
		auditFields:  false,
		defaultActor: "system",
	}
	if err := r.Delete(context.Background(), int64(1)); err != nil {
		t.Fatalf("expected hard delete fallback to succeed, got %v", err)
	}
	if !m.deleteCalled {
		t.Fatalf("expected hard delete fallback to call Delete")
	}
}

func TestRepo_DeleteFallbackSoftDeleteReturnsNotFoundWhenRecordMissing(t *testing.T) {
	m := &deleteFallbackModel{count: 0}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	err := r.Delete(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected soft delete fallback NotFound, got %v", err)
	}
	if m.updateCalled {
		t.Fatalf("expected missing soft delete fallback to skip UpdateValues")
	}

	m = &deleteFallbackModel{count: 0}
	r = &Repo[*auditedEntity, int64]{
		model:        m,
		auditFields:  false,
		defaultActor: "system",
	}
	err = r.Delete(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected hard delete fallback NotFound, got %v", err)
	}
	if m.deleteCalled {
		t.Fatalf("expected missing hard delete fallback to skip Delete")
	}
}

func TestRepo_PurgeFallbacksToPlainModelWhenResultUnsupported(t *testing.T) {
	m := &deleteFallbackModel{count: 1}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    false,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	if err := r.Purge(context.Background(), int64(1)); err != nil {
		t.Fatalf("expected purge fallback to succeed, got %v", err)
	}
	if !m.deleteCalled {
		t.Fatalf("expected purge fallback to call Delete")
	}
}

func TestRepo_PurgeFallbackReturnsNotFoundWhenRecordMissing(t *testing.T) {
	m := &deleteFallbackModel{count: 0}
	r := &Repo[*auditedEntity, int64]{
		model:          m,
		softDelete:     true,
		auditFields:    false,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	err := r.Purge(context.Background(), int64(1))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected purge fallback NotFound, got %v", err)
	}
	if m.deleteCalled {
		t.Fatalf("expected missing purge fallback to skip Delete")
	}
}

func TestRepo_DeleteAllFallbacksToPlainModelWhenResultUnsupported(t *testing.T) {
	m := &deleteFallbackModel{count: 2}
	session := &deleteModeSession{model: m}
	r := &Repo[*auditedEntity, int64]{
		orm:            session,
		model:          m,
		softDelete:     true,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	if err := r.DeleteAll(context.Background(), []int64{1, 2}); err != nil {
		t.Fatalf("expected batch soft delete fallback to succeed, got %v", err)
	}
	if !m.updateCalled {
		t.Fatalf("expected batch soft delete fallback to call UpdateValues")
	}

	m = &deleteFallbackModel{count: 2}
	session = &deleteModeSession{model: m}
	r = &Repo[*auditedEntity, int64]{
		orm:          session,
		model:        m,
		auditFields:  false,
		defaultActor: "system",
	}
	if err := r.DeleteAll(context.Background(), []int64{1, 2}); err != nil {
		t.Fatalf("expected batch hard delete fallback to succeed, got %v", err)
	}
	if !m.deleteCalled {
		t.Fatalf("expected batch hard delete fallback to call Delete")
	}
}

type deleteModeSession struct {
	model orm.IModel
}

func (s *deleteModeSession) Capabilities() orm.Capabilities       { return nil }
func (s *deleteModeSession) WithContext(context.Context) orm.IOrm { return s }
func (s *deleteModeSession) Model(*orm.ModelMeta) (orm.IModel, error) {
	return s.model, nil
}
func (s *deleteModeSession) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *deleteModeSession) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *deleteModeSession) Database() db.IDatabase { return nil }
func (s *deleteModeSession) Commit() error          { return nil }
func (s *deleteModeSession) Rollback() error        { return nil }

type deleteFallbackModel struct {
	updateCalled bool
	deleteCalled bool
	count        int64
}

func (m *deleteFallbackModel) Meta() *orm.ModelMeta { return &orm.ModelMeta{Table: "t"} }
func (m *deleteFallbackModel) Capabilities() orm.Capabilities {
	return orm.Capabilities{}
}
func (m *deleteFallbackModel) Dialect() repoTestDialect { return unknownRepoTestDialect() }
func (m *deleteFallbackModel) First(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}
func (m *deleteFallbackModel) Find(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}
func (m *deleteFallbackModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return m.count, nil
}
func (m *deleteFallbackModel) Create(context.Context, ...any) error { panic("not used") }
func (m *deleteFallbackModel) Save(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}
func (m *deleteFallbackModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	m.updateCalled = true
	return nil
}
func (m *deleteFallbackModel) Delete(context.Context, ...orm.QueryOption) error {
	m.deleteCalled = true
	return nil
}
func (m *deleteFallbackModel) Association(any, string) orm.IAssociation { return nil }
