package authz

import (
	"context"
	"testing"

	coreauth "gochen/auth"
	"gochen/contextx"
	"gochen/contextx/field"
	"gochen/errors"
	"gochen/testkit/require"
)

func TestRuntimePrincipalPublishesRootIdentity(t *testing.T) {
	ctx, err := contextx.WithTenantID(context.Background(), "active-tenant")
	require.NoError(t, err)
	ctx, err = WithPrincipal(ctx, Principal{
		SubjectID: " alice ", HomeTenantID: "home-tenant", ActiveScopeID: 101,
		Permissions: []string{"order:api:read"}, PermissionScopes: []string{"tenant"},
	})
	require.NoError(t, err)
	root, err := coreauth.RequirePrincipal(ctx)
	require.NoError(t, err)
	require.Equal(t, "alice", root.SubjectID)
	require.Equal(t, "active-tenant", root.TenantID)
	require.Equal(t, int64(101), root.ScopeID)
	require.False(t, root.System)
	principal, err := RequirePrincipal(ctx)
	require.NoError(t, err)
	require.Equal(t, "home-tenant", principal.HomeTenantID)
	require.Equal(t, []string{"order:api:read"}, principal.Permissions)
	principal.Permissions[0] = "*:*:*"
	again, err := RequirePrincipal(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"order:api:read"}, again.Permissions)
}

func TestCoreSystemIdentityIsSharedByRuntimeChecks(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: "alice", ActiveScopeID: 101})
	systemCtx := coreauth.AsSystem(ctx, "background execution")
	principal, err := RequirePrincipal(systemCtx)
	require.NoError(t, err)
	require.True(t, principal.IsSystem)
	require.Equal(t, "alice", principal.SubjectID)
	require.Equal(t, int64(101), principal.ActiveScopeID)
	checker := NewActionChecker()
	require.NoError(t, checker.RequireAction(systemCtx, "order:api:update"))
	rbacPolicy, err := coreauth.NewPermissionPolicy(checker)
	require.NoError(t, err)
	rbac, err := coreauth.New(coreauth.Options{Policy: rbacPolicy})
	require.NoError(t, err)
	fullPolicy, err := coreauth.NewPermissionPolicy(checker, coreauth.WithScope(NewScopeResolver("department", nil)))
	require.NoError(t, err)
	full, err := coreauth.New(coreauth.Options{Policy: fullPolicy})
	require.NoError(t, err)
	for _, authorizer := range []coreauth.IAuthorizer{rbac, full} {
		decision, err := authorizer.Authorize(systemCtx, coreauth.Request{Permission: "order:api:update"})
		require.NoError(t, err)
		require.True(t, decision.Allowed)
		require.True(t, decision.Spec.IsAll())
	}
	condition, _, err := NewScopeResolver("department", nil).ResolveSpec(systemCtx, coreauth.Request{})
	require.NoError(t, err)
	require.True(t, condition.IsAll())
	info, ok := coreauth.SystemInfoFromContext(systemCtx)
	require.True(t, ok)
	require.Equal(t, "alice", info.Initiator)
	require.Equal(t, "background execution", info.Reason)
	original, err := RequirePrincipal(ctx)
	require.NoError(t, err)
	require.False(t, original.IsSystem)
}

func TestRootIdentityChangesDiscardAttachedPermissions(t *testing.T) {
	for _, tc := range []struct {
		name string
		root coreauth.Principal
	}{
		{name: "subject", root: coreauth.Principal{SubjectID: "bob", TenantID: "tenant", ScopeID: 101}},
		{name: "scope", root: coreauth.Principal{SubjectID: "alice", TenantID: "tenant", ScopeID: 202}},
		{name: "tenant", root: coreauth.Principal{SubjectID: "alice", TenantID: "other", ScopeID: 101}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := contextx.WithTenantID(context.Background(), "tenant")
			require.NoError(t, err)
			ctx, err = WithPrincipal(ctx, Principal{
				SubjectID: "alice", ActiveScopeID: 101, ActiveBindingID: 12,
				Permissions: []string{"*:*:*"}, PermissionScopes: []string{"platform"},
			})
			require.NoError(t, err)
			derived, err := coreauth.WithPrincipal(ctx, tc.root)
			require.NoError(t, err)
			principal, err := RequirePrincipal(derived)
			require.NoError(t, err)
			require.Equal(t, tc.root.SubjectID, principal.SubjectID)
			require.Equal(t, tc.root.ScopeID, principal.ActiveScopeID)
			require.Empty(t, principal.Permissions)
			require.Empty(t, principal.PermissionScopes)
			require.Equal(t, int64(0), principal.ActiveBindingID)
			require.True(t, errors.Is(NewActionChecker().RequireAction(derived, "order:api:update"), errors.Forbidden))
			original, err := RequirePrincipal(ctx)
			require.NoError(t, err)
			require.Equal(t, []string{"*:*:*"}, original.Permissions)

			root, err := coreauth.RequirePrincipal(ctx)
			require.NoError(t, err)
			returned, err := coreauth.WithPrincipal(derived, root)
			require.NoError(t, err)
			principal, err = RequirePrincipal(returned)
			require.NoError(t, err)
			require.Empty(t, principal.Permissions)
			require.Empty(t, principal.PermissionScopes)
			require.Equal(t, int64(0), principal.ActiveBindingID)
			require.True(t, errors.Is(NewActionChecker().RequireAction(returned, "order:api:update"), errors.Forbidden))

			returned, err = WithPrincipal(returned, Principal{SubjectID: root.SubjectID,
				ActiveScopeID: root.ScopeID, Permissions: []string{"order:api:read"}})
			require.NoError(t, err)
			require.NoError(t, NewActionChecker().RequireAction(returned, "order:api:read"))
			require.True(t, errors.Is(NewActionChecker().RequireAction(returned, "order:api:update"), errors.Forbidden))
		})
	}
}

func TestRuntimeReauthenticationClearsSystemIdentityAndMetadata(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: "alice", ActiveScopeID: 101})
	ctx = coreauth.AsSystem(ctx, "old execution")
	ctx, err := WithPrincipal(ctx, Principal{SubjectID: "bob", ActiveScopeID: 202})
	require.NoError(t, err)
	root, err := coreauth.RequirePrincipal(ctx)
	require.NoError(t, err)
	require.Equal(t, "bob", root.SubjectID)
	require.False(t, root.System)
	_, ok := coreauth.SystemInfoFromContext(ctx)
	require.False(t, ok)
	require.True(t, errors.Is(NewActionChecker().RequireAction(ctx, "order:api:update"), errors.Forbidden))
	systemCtx := coreauth.AsSystem(ctx, "new execution")
	info, ok := coreauth.SystemInfoFromContext(systemCtx)
	require.True(t, ok)
	require.Equal(t, "bob", info.Initiator)
	require.Equal(t, "new execution", info.Reason)
}

func TestCurrentTenantChangeRequiresNewPermissionDetails(t *testing.T) {
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	ctx, err = WithPrincipal(ctx, Principal{SubjectID: "alice", ActiveScopeID: 101,
		Permissions: []string{"order:api:read"}})
	require.NoError(t, err)
	changed, err := contextx.WithTenantID(ctx, "tenant-b")
	require.NoError(t, err)
	principal, err := RequirePrincipal(changed)
	require.NoError(t, err)
	require.Equal(t, "alice", principal.SubjectID)
	require.Empty(t, principal.Permissions)
	require.True(t, errors.Is(NewActionChecker().RequireAction(changed, "order:api:read"), errors.Forbidden))
	require.NoError(t, NewActionChecker().RequireAction(ctx, "order:api:read"))

	changed, err = WithPrincipal(changed, Principal{SubjectID: "alice", ActiveScopeID: 101,
		Permissions: []string{"order:api:read"}})
	require.NoError(t, err)
	root, err := coreauth.RequirePrincipal(changed)
	require.NoError(t, err)
	require.Equal(t, "tenant-b", root.TenantID)
	require.NoError(t, NewActionChecker().RequireAction(changed, "order:api:read"))
}

func TestTenantRoundTripDoesNotRestorePermissionDetails(t *testing.T) {
	for _, bind := range []struct {
		name       string
		withTenant func(context.Context, string) (context.Context, error)
	}{
		{name: "contextx", withTenant: contextx.WithTenantID},
		{name: "field", withTenant: field.WithTenantID},
	} {
		t.Run(bind.name, func(t *testing.T) {
			ctx, err := bind.withTenant(context.Background(), "tenant-a")
			require.NoError(t, err)
			ctx, err = WithPrincipal(ctx, Principal{SubjectID: "alice", ActiveScopeID: 101,
				Permissions: []string{"order:api:read"}})
			require.NoError(t, err)
			unchanged, err := bind.withTenant(ctx, " tenant-a ")
			require.NoError(t, err)
			require.NoError(t, NewActionChecker().RequireAction(unchanged, "order:api:read"))
			changed, err := bind.withTenant(ctx, "tenant-b")
			require.NoError(t, err)
			returned, err := bind.withTenant(changed, "tenant-a")
			require.NoError(t, err)
			require.True(t, errors.Is(NewActionChecker().RequireAction(returned, "order:api:read"), errors.Forbidden))
			require.NoError(t, NewActionChecker().RequireAction(ctx, "order:api:read"))
		})
	}
}

func TestSameRootIdentityAndSystemExecutionKeepPermissionDetails(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: "alice", ActiveScopeID: 101,
		Permissions: []string{"order:api:read"}})
	root, err := coreauth.RequirePrincipal(ctx)
	require.NoError(t, err)
	ctx, err = coreauth.WithPrincipal(ctx, root)
	require.NoError(t, err)
	require.NoError(t, NewActionChecker().RequireAction(ctx, "order:api:read"))
	principal, err := RequirePrincipal(coreauth.AsSystem(ctx, "background execution"))
	require.NoError(t, err)
	require.Equal(t, []string{"order:api:read"}, principal.Permissions)
}

func TestTracingUserIDDoesNotEstablishAuthorizationPrincipal(t *testing.T) {
	ctx, err := contextx.WithUserID(context.Background(), "alice")
	require.NoError(t, err)
	_, ok := PrincipalFromContext(ctx)
	require.False(t, ok)
	require.True(t, errors.Is(NewActionChecker().RequireAction(ctx, "order:api:read"), errors.Unauthorized))
}
