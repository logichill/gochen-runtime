package repo

import (
	"strings"

	"gochen/auth/scoped"
	"gochen/errors"
)

// L3 数据范围与写约束在仓储侧的落地。
//
// Repo 不被装饰器包装，而是在构造期通过 WithScope 显式声明范围列；
// 写路径从受控通道读取约束并合并进原子 WHERE。

var _ scoped.IScopeDeclarationProbe = (*Repo[dummyScopeEntity, int64])(nil)
var _ scoped.IConstraintWriteProbe = (*Repo[dummyScopeEntity, int64])(nil)

// IResourceKindProbe 同样只按接口断言被发现：requireScopeDeclaration 用它比对
// Config.EntityType 与仓储资源类型，签名漂移会让那处断言退化成启动期
// Unsupported——本可以是编译错误的问题，被推迟到了运行期。
var _ scoped.IResourceKindProbe = (*Repo[dummyScopeEntity, int64])(nil)

// 边界探针按接口断言被 L3 发现：签名一旦漂移，装饰器只会静默降级成逐条解析，
// 不会有任何编译或运行期报错。用编译期断言把这种漂移钉死在本包。
var _ scoped.IResourceBoundaryReader[int64] = (*Repo[dummyScopeEntity, int64])(nil)
var _ scoped.IBatchResourceBoundaryReader[int64] = (*Repo[dummyScopeEntity, int64])(nil)
var _ scoped.IDeletedResourceBoundaryReader[int64] = (*Repo[dummyScopeEntity, int64])(nil)

// dummyScopeEntity 仅用于上面的编译期接口断言。
type dummyScopeEntity struct{}

func (dummyScopeEntity) GetID() int64       { return 0 }
func (dummyScopeEntity) GetVersion() uint64 { return 0 }

// HasScopeDeclaration 返回仓储是否已显式声明范围列。
//
// 供 L3 装配层做构造期断言：未声明范围列的 Repo 不会消费写约束，
// 把它装进 L3 会导致约束被静默丢弃、请求全部放行。
func (r *Repo[T, ID]) HasScopeDeclaration() bool {
	if r == nil {
		return false
	}
	return strings.TrimSpace(r.accessColumns.managedScope) != ""
}

// validateAccessColumns 校验显式声明的安全列都能落到实体字段上。
//
// 显式声明模型的代价是"声明了就必须兑现"：列名写错或实体缺字段时，
// 过滤条件会静默失效或写入无法盖戳。文档 §1.5.2 要求这类问题在构造期暴露，
// 而不是等到某条请求命中才报错。
func (r *Repo[T, ID]) validateAccessColumns() error {
	schema := r.accessSchema()

	requireMapped := func(field dataScopeField, name string) error {
		if field.column == "" || field.hasField() {
			return nil
		}
		return errors.NewCode(errors.InvalidInput,
			"repository declares a security column that does not map to any entity field").
			WithContext("resource_kind", r.resourceKind).
			WithContext("field", name).
			WithContext("column", field.column)
	}

	for _, entry := range []struct {
		field dataScopeField
		name  string
	}{
		{schema.managedScope, "managed_scope_id"},
		{schema.isolation, "isolation"},
		{schema.ownerID, "owner_id"},
		{schema.version, "revision"},
	} {
		if err := requireMapped(entry.field, entry.name); err != nil {
			return err
		}
	}

	// 范围列与 revision 列必须成对声明：约束写（update/delete/purge）以 revision
	// 做乐观锁原子求交，缺了它每一次约束写都会在运行期报
	// "write constraint revision is required"——等于 L3 写路径整体不可用。
	if schema.managedScope.column != "" && schema.version.column == "" {
		return errors.NewCode(errors.InvalidInput,
			"repository declares a managed scope column but no revision column; "+
				"constrained writes require a revision for optimistic-lock intersection (use ormrepo.WithScope with Revision set)").
			WithContext("resource_kind", r.resourceKind).
			WithContext("managed_scope_column", schema.managedScope.column)
	}
	return nil
}
