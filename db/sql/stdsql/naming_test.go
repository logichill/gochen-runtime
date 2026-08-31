package stdsql_test

import (
	"testing"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/testkit/assert"
	"gochen/testkit/require"
)

func TestDBNamingConvention(t *testing.T) {
	// Default when Naming is nil
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	provider, ok := database.(db.INamingConventionProvider)
	require.True(t, ok)
	assert.Equal(t, db.DefaultNamingConvention(), provider.NamingConvention())

	// Custom when Naming is configured
	customNaming := db.NamingConvention{
		TenantColumn: "custom_tenant",
		ScopeColumn:  "custom_scope",
	}
	dbWithNaming, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
		Naming:   &customNaming,
	})
	require.NoError(t, err)
	defer func() { _ = dbWithNaming.Close() }()

	providerCustom, ok := dbWithNaming.(db.INamingConventionProvider)
	require.True(t, ok)
	assert.Equal(t, "custom_tenant", providerCustom.NamingConvention().TenantColumn)
	assert.Equal(t, "custom_scope", providerCustom.NamingConvention().ScopeColumn)
}
