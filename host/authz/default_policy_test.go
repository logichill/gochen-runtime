package authz

import (
	"context"
	"testing"

	coreauth "gochen/auth"
	"gochen/auth/scoped"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/testkit/require"
)

// 默认 L1 / L3 判定实现的行为特征测试。

func principalCtx(t *testing.T, principal Principal) context.Context {
	t.Helper()
	ctx, err := WithPrincipal(context.Background(), principal)
	require.NoError(t, err)
	return ctx
}

// ---------- L1 NewActionChecker ----------

func TestActionChecker_GrantsExactAndWildcardCodes(t *testing.T) {
	checker := NewActionChecker()
	ctx := principalCtx(t, Principal{
		SubjectID:   "7",
		Permissions: []string{"order:api:read", "user:api:*"},
	})

	require.NoError(t, checker.RequireAction(ctx, "order:api:read"))
	require.NoError(t, checker.RequireAction(ctx, "user:api:update"))
	require.True(t, errors.Is(checker.RequireAction(ctx, "order:api:delete"), errors.Forbidden))
}

func TestActionChecker_SystemPrincipalBypasses(t *testing.T) {
	checker := NewActionChecker()
	ctx := principalCtx(t, Principal{SubjectID: "1", IsSystem: true})
	require.NoError(t, checker.RequireAction(ctx, "order:api:delete"))
}

// 错误码必须与 action.IActionChecker 声明的契约一致：缺主体是 401 不是 403，
// 码拼错是 400 不是 403——否则下游把装配错误当权限问题排查。
func TestActionChecker_ErrorContract(t *testing.T) {
	checker := NewActionChecker()

	err := checker.RequireAction(context.Background(), "order:api:read")
	require.True(t, errors.Is(err, errors.Unauthorized))

	ctx := principalCtx(t, Principal{SubjectID: "7", Permissions: []string{"order:api:read"}})
	err = checker.RequireAction(ctx, "not-a-three-segment-code")
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestScopeResolverUsesProjectDimension(t *testing.T) {
	a, err := scoped.New(NewActionChecker(), NewScopeResolver("department", nil))
	require.NoError(t, err)
	ctx := principalCtx(t, Principal{SubjectID: "7", ActiveScopeID: 101, Permissions: []string{"order:api:update"}})
	d, err := a.Authorize(ctx, coreauth.Request{Resource: "order", Action: "update", Permission: "order:api:update", IDs: []string{"1"}})
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.Equal(t, "eq(dim:department,101)", d.Spec.Canonical())
	type document struct {
		ID         string `entity:"id"`
		Department int64  `dim:"department"`
	}
	for _, tc := range []struct {
		department int64
		allowed    bool
	}{{101, true}, {202, false}, {0, false}} {
		matched, err := spec.NewInterpreter().Match(d.Spec, &document{ID: "1", Department: tc.department})
		require.NoError(t, err)
		require.Equal(t, tc.allowed, matched)
	}
	_, err = a.Authorize(context.Background(), coreauth.Request{Permission: "order:api:update"})
	require.True(t, errors.Is(err, errors.Unauthorized))
	d, err = a.Authorize(ctx, coreauth.Request{Permission: "order:api:delete"})
	require.NoError(t, err)
	require.False(t, d.Allowed)
}

func TestScopeResolverFailsClosedAndRespectsBoundScope(t *testing.T) {
	r := NewScopeResolver("department", scoped.DataScopeResolverFunc(func(context.Context) (scoped.DataScope, error) { return scoped.Filtered(101, 202), nil }))
	ctx := principalCtx(t, Principal{SubjectID: "7", ActiveScopeID: 101})
	s, _, err := r.ResolveSpec(ctx, coreauth.Request{})
	require.NoError(t, err)
	require.Equal(t, "in(dim:department,101,202)", s.Canonical())
	ctx, err = scoped.WithDataScope(ctx, scoped.DenyAll())
	require.NoError(t, err)
	s, _, err = r.ResolveSpec(ctx, coreauth.Request{})
	require.NoError(t, err)
	require.True(t, s.IsNone())
	s, _, err = NewScopeResolver("department", nil).ResolveSpec(context.Background(), coreauth.Request{})
	require.NoError(t, err)
	require.True(t, s.IsNone())
	ctx, err = scoped.WithDataScope(ctx, scoped.Global())
	require.NoError(t, err)
	s, _, err = r.ResolveSpec(ctx, coreauth.Request{})
	require.NoError(t, err)
	require.True(t, s.IsAll())
}
