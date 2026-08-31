package repo

import (
	"context"
	"testing"

	"gochen/app/query"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/errors"
)

// notFoundModel 模拟底层 ORM 返回 NotFound 的情况。
type notFoundModel struct{}

// Meta result：返回的实例（类型：*orm.ModelMeta）。
//
// 返回：
func (m *notFoundModel) Meta() *orm.ModelMeta { return &orm.ModelMeta{Table: "t"} }

// Capabilities result：测试返回值（类型：orm.Capabilities）。
//
// 返回：
func (m *notFoundModel) Capabilities() orm.Capabilities { return orm.Capabilities{} }

func (m *notFoundModel) Dialect() repoTestDialect { return unknownRepoTestDialect() }

// First ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - dest：目标对象（用于写入/拷贝结果）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	_ = opts
	return errors.NewCode(errors.NotFound, "record not found")
}

// Find 从存储中查询对象。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - dest：目标对象（用于写入/拷贝结果）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) Find(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	_ = opts
	return nil // Find 返回空结果，不是错误
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
func (m *notFoundModel) Count(ctx context.Context, opts ...orm.QueryOption) (int64, error) {
	_ = ctx
	_ = opts
	return 0, nil
}

// Create 创建对象并写入存储。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - values：可变参数（类型：...any）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) Create(ctx context.Context, values ...any) error {
	panic("not used")
}

// Save ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - value：值（待写入/比较/校验的值）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) Save(ctx context.Context, value any, opts ...orm.QueryOption) error {
	panic("not used")
}

// SaveWithResult ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - value：值（待写入/比较/校验的值）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - result1：返回结果（类型：interface{ RowsAffected() (int64, error) }）
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) SaveWithResult(ctx context.Context, value any, opts ...orm.QueryOption) (interface{ RowsAffected() (int64, error) }, error) {
	panic("not used")
}

// UpdateValues 更新对象并写入存储。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - values：属性/参数集合（类型：map[string]any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) UpdateValues(ctx context.Context, values map[string]any, opts ...orm.QueryOption) error {
	panic("not used")
}

// UpdateValuesWithResult 更新对象并写入存储。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - values：属性/参数集合（类型：map[string]any）
// - opts：可选参数/配置项
//
// 返回：
// - result1：返回结果（类型：interface{ RowsAffected() (int64, error) }）
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) UpdateValuesWithResult(ctx context.Context, values map[string]any, opts ...orm.QueryOption) (interface{ RowsAffected() (int64, error) }, error) {
	panic("not used")
}

// Delete 删除对象并同步到存储。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) Delete(ctx context.Context, opts ...orm.QueryOption) error {
	panic("not used")
}

// DeleteWithResult 删除对象并同步到存储。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - opts：可选参数/配置项
//
// 返回：
// - result1：返回结果（类型：interface{ RowsAffected() (int64, error) }）
// - err：错误信息（nil 表示成功）
func (m *notFoundModel) DeleteWithResult(ctx context.Context, opts ...orm.QueryOption) (interface{ RowsAffected() (int64, error) }, error) {
	panic("not used")
}

// Association 返回关联查询对象（测试 stub）。
//
// 参数：
// - owner：关联主对象
// - name：关联名
//
// 返回：
// - result：关联查询对象
func (m *notFoundModel) Association(owner any, name string) orm.IAssociation {
	_ = owner
	_ = name
	return nil
}

// TestContract_Get_NotFound 验证 Contract Get NotFound。
func TestContract_Get_NotFound(t *testing.T) {
	m := &notFoundModel{}
	r := &Repo[*auditedTestEntity, int64]{
		model:          m,
		softDelete:     false,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	_, err := r.Get(context.Background(), int64(999))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound error, got: %v (code: %v)", err, errors.Code(err))
	}
}

// TestContract_QueryOne_NotFound 验证 Contract QueryOne NotFound。
func TestContract_QueryOne_NotFound(t *testing.T) {
	m := &notFoundModel{}
	r := &Repo[*auditedTestEntity, int64]{
		model:          m,
		softDelete:     false,
		auditFields:    true,
		defaultActor:   "system",
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at", DeletedBy: "deleted_by"},
	}

	_, err := r.QueryOne(context.Background(), query.QueryOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound error, got: %v (code: %v)", err, errors.Code(err))
	}
}

// auditedTestEntity 是用于测试的实体类型（避免与 optimistic_lock_test.go 中的 auditedEntity 冲突）。
type auditedTestEntity struct {
	*audited.AuditedEntity[int64]
}

// Validate 校验输入是否满足约束。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (e *auditedTestEntity) Validate() error { return nil }
