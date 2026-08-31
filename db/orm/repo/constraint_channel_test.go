package repo

import (
	"context"
	"testing"

	"gochen/testkit/require"

	"gochen/auth/scoped"
	"gochen/errors"
)

// 受控约束通道的仓储侧回归。
//
// 这些用例锁定的是"通道断了会怎样"——此前 ConstraintFrom 在生产代码中
// 没有任何消费方，装饰器算出的写约束被静默丢弃，行级授权整体失效。

func newScopeDeclaredRepo(model *capturingDataScopeModel) *Repo[*scopedRepoEntity, int64] {
	return &Repo[*scopedRepoEntity, int64]{
		model:         model,
		resourceKind:  "document",
		accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
	}
}

func scopeBoundContext(t *testing.T, scopeID int64) context.Context {
	t.Helper()
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(scopeID))
	require.NoError(t, err)
	return ctx
}

// 声明了范围列却完全没经过 L3 装饰器：必须拒绝，且不得落库。
func TestConstraintChannel_WriteWithoutProviderIsRejected(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := newScopeDeclaredRepo(model)
	ctx := scopeBoundContext(t, 101)

	err := repository.Create(ctx, &scopedRepoEntity{ID: 1})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)

	err = repository.Update(ctx, &scopedRepoEntity{ID: 1})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))

	err = repository.Delete(ctx, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
}

// 嵌套写防护：Hook 内写另一种实体时，约束提供者匹配不到该实体类型，
// 同样拒绝——不拿着订单的约束去写日志，也不静默放行。
func TestConstraintChannel_ForeignEntityTypeIsRejected(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := newScopeDeclaredRepo(model)

	ctx := scoped.WithConstraint(scopeBoundContext(t, 101),
		scoped.SingleEntityConstraint("audit_log", scoped.WriteConstraint{
			Resources: []scoped.ResourceConstraint{{Kind: "audit_log", ResourceID: "1"}},
		}))

	err := repository.Create(ctx, &scopedRepoEntity{ID: 1})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)
}

// 命中约束时正常放行，并把范围归属写进实体。
func TestConstraintChannel_MatchingConstraintAllowsWrite(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := newScopeDeclaredRepo(model)

	ctx := withScopedWriteConstraint(scopeBoundContext(t, 101), "document", "1", 101)
	entity := &scopedRepoEntity{ID: 1}

	require.NoError(t, repository.Create(ctx, entity))
	require.Equal(t, int64(101), entity.ManagedScopeID)
	require.Same(t, entity, model.created)
}

// 未声明范围列的仓储（L0/L1/L2）不受通道约束，普通写路径原样可用。
func TestConstraintChannel_UndeclaredRepoIsUnaffected(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document"}

	require.NoError(t, repository.Create(context.Background(), &scopedRepoEntity{ID: 1}))
	require.NotNil(t, model.created)
}
