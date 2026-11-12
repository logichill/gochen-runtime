package authz

import (
	"context"
	"testing"

	"gochen/auth"
	"gochen/contextx"
	"gochen/testkit/require"
)

func TestActiveScopeBindingTracksIdentity(t *testing.T) {
	for _, change := range []string{"subject", "scope", "tenant", "system", "clear", "empty", "mismatch"} {
		t.Run(change, func(t *testing.T) {
			ctx, err := contextx.WithTenantID(context.Background(), "A")
			require.NoError(t, err)
			ctx, err = WithPrincipal(ctx, Principal{SubjectID: "alice", ActiveScopeID: 7})
			require.NoError(t, err)
			ctx = BindActiveScopeContext(ctx, 7, "platform")
			require.Equal(t, int64(7), ActiveScopeIDFromContext(ctx))
			require.Equal(t, "platform", ActiveScopeKindFromContext(ctx))
			derived := ctx
			switch change {
			case "subject":
				derived, err = WithPrincipal(ctx, Principal{SubjectID: "bob", ActiveScopeID: 7})
			case "scope":
				derived, err = WithPrincipal(ctx, Principal{SubjectID: "alice", ActiveScopeID: 99})
			case "tenant":
				derived, err = contextx.WithTenantID(ctx, "B")
			case "system":
				derived = auth.AsSystem(ctx, "job")
			case "clear":
				derived = ClearActiveScopeContext(ctx)
			case "empty":
				derived = BindActiveScopeContext(ctx, 0, "")
			case "mismatch":
				derived = BindActiveScopeContext(ctx, 99, "tenant")
			}
			require.NoError(t, err)
			require.Equal(t, int64(0), ActiveScopeIDFromContext(derived))
			require.Equal(t, "", ActiveScopeKindFromContext(derived))
			require.Equal(t, int64(7), ActiveScopeIDFromContext(ctx))
		})
	}
}
