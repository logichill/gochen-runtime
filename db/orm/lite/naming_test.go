package lite_test

import (
	"testing"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func TestLiteOrmNamingConvention(t *testing.T) {
	// 1. Inherit from database
	customNaming := db.NamingConvention{
		TenantColumn: "tenant_code",
		ScopeColumn:  "dept_id",
	}
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
		Naming:   &customNaming,
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	ormEngine, err := lite.New(database)
	require.NoError(t, err)

	provider, ok := ormEngine.(db.INamingConventionProvider)
	require.True(t, ok)
	assert.Equal(t, "tenant_code", provider.NamingConvention().TenantColumn)
	assert.Equal(t, "dept_id", provider.NamingConvention().ScopeColumn)

	// 2. Explicit WithNamingConvention override
	overrideNaming := db.NamingConvention{
		TenantColumn: "override_tenant",
		ScopeColumn:  "override_scope",
	}
	ormEngineOverride, err := lite.New(database, lite.WithNamingConvention(overrideNaming))
	require.NoError(t, err)

	providerOverride, ok := ormEngineOverride.(db.INamingConventionProvider)
	require.True(t, ok)
	assert.Equal(t, "override_tenant", providerOverride.NamingConvention().TenantColumn)
	assert.Equal(t, "override_scope", providerOverride.NamingConvention().ScopeColumn)
}
