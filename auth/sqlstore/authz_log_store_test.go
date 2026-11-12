package sqlstore_test

import (
	"context"
	"testing"
	"time"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/auth/sqlstore"
	"gochen-runtime/db/sql/stdsql"
	auth "gochen/auth"
	"gochen/db"
	"gochen/errors"
	"gochen/gen"
)

type authzLogTestGenerator struct {
	next func() (string, error)
}

func (g *authzLogTestGenerator) Next() (string, error) {
	return g.next()
}

func TestAuthzLogStore_SaveAndList(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", gen.NewUUIDGenerator())
	require.NoError(t, err)

	entry := auth.AuthzLogEntry{
		ID:              "log-1",
		Type:            auth.AuthzLogEntryTypeDecision,
		DecisionID:      "decision-1",
		PrincipalID:     "7",
		Action:          "doc:read",
		Effect:          "allow",
		SnapshotVersion: "snap-v1",
		SnapshotKey:     "default",
		Consistency:     auth.ConsistencyModeStrong,
		LatencyMs:       12,
		Resources: []auth.AuthzLoggedResource{{
			Kind:     " doc ",
			ID:       " doc-1 ",
			TenantID: " tenant-a ",
			OwnerID:  " owner-a ",
		}},
		MatchedRules: []string{"rule-1"},
		Execution:    auth.ExecutionMetadata{RequestID: "req-1"},
		Metadata:     map[string]any{"cache_hit_reason": "fresh"},
		Timestamp:    time.Now(),
	}
	require.NoError(t, store.SaveAuthzLogEntry(context.Background(), entry))

	entries, err := store.ListAuthzLogEntries(context.Background(), auth.AuthzLogEntryTypeDecision, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "decision-1", entries[0].DecisionID)
	require.Equal(t, "doc:read", entries[0].Action)
	require.Equal(t, "doc", entries[0].Resources[0].Kind)
	require.Equal(t, "doc-1", entries[0].Resources[0].ID)
	require.Empty(t, entries[0].Resources[0].TenantID)
	require.Empty(t, entries[0].Resources[0].OwnerID)
	require.Equal(t, []string{"rule-1"}, entries[0].MatchedRules)
	require.Equal(t, "req-1", entries[0].Execution.RequestID)
	require.Equal(t, "fresh", entries[0].Metadata["cache_hit_reason"])
}

func TestAuthzLogStore_CustomSanitizerCanKeepTenantAndOwnerID(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", gen.NewUUIDGenerator())
	require.NoError(t, err)
	ctx, err := auth.WithResourceLogSanitizer(context.Background(), auth.ResourceLogSanitizerFunc(func(ctx context.Context, resource auth.AuthzLoggedResource) auth.AuthzLoggedResource {
		resource.TenantID = "tenant-a"
		resource.OwnerID = "owner-a"
		return resource
	}))
	require.NoError(t, err)

	require.NoError(t, store.SaveAuthzLogEntry(ctx, auth.AuthzLogEntry{
		ID:   "log-sanitized",
		Type: auth.AuthzLogEntryTypeDecision,
		Resources: []auth.AuthzLoggedResource{{
			Kind:     "doc",
			ID:       "doc-1",
			TenantID: "raw-tenant",
			OwnerID:  "raw-owner",
		}},
		Timestamp: time.Now(),
	}))

	entries, err := store.ListAuthzLogEntries(context.Background(), auth.AuthzLogEntryTypeDecision, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "tenant-a", entries[0].Resources[0].TenantID)
	require.Equal(t, "owner-a", entries[0].Resources[0].OwnerID)
}

func TestAuthzLogStore_RejectsUnsafeTableName(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	store, err := sqlstore.NewAuthzLogStore(database, "authz_logs; DROP TABLE authz_logs", gen.NewUUIDGenerator())
	require.Error(t, err)
	require.Nil(t, store)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestAuthzLogStore_SaveGeneratesIDForEmptyID(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", gen.NewUUIDGenerator())
	require.NoError(t, err)

	require.NoError(t, store.SaveAuthzLogEntry(context.Background(), auth.AuthzLogEntry{
		Type:       auth.AuthzLogEntryTypeDecision,
		DecisionID: "decision-1",
		Timestamp:  time.Now(),
	}))
	require.NoError(t, store.SaveAuthzLogEntry(context.Background(), auth.AuthzLogEntry{
		Type:       auth.AuthzLogEntryTypeDecision,
		DecisionID: "decision-2",
		Timestamp:  time.Now(),
	}))

	entries, err := store.ListAuthzLogEntries(context.Background(), auth.AuthzLogEntryTypeDecision, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.NotEmpty(t, entries[0].ID)
	require.NotEmpty(t, entries[1].ID)
	require.NotEqual(t, entries[0].ID, entries[1].ID)
}

func TestAuthzLogStore_IDGeneratorBoundaries(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	var nilGenerator gen.IGenerator[string]
	var typedNil *authzLogTestGenerator
	for name, generator := range map[string]gen.IGenerator[string]{
		"nil":       nilGenerator,
		"typed nil": typedNil,
	} {
		t.Run(name, func(t *testing.T) {
			store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", generator)
			require.Error(t, err)
			require.True(t, errors.Is(err, errors.InvalidInput))
			require.Nil(t, store)
		})
	}

	t.Run("generator failure", func(t *testing.T) {
		store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", &authzLogTestGenerator{next: func() (string, error) {
			return "", errors.NewCode(errors.Database, "id backend unavailable")
		}})
		require.NoError(t, err)
		err = store.SaveAuthzLogEntry(context.Background(), auth.AuthzLogEntry{})
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.Dependency))
	})

	t.Run("empty generated id", func(t *testing.T) {
		store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", &authzLogTestGenerator{next: func() (string, error) {
			return "   ", nil
		}})
		require.NoError(t, err)
		err = store.SaveAuthzLogEntry(context.Background(), auth.AuthzLogEntry{})
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.Internal))
	})
}

func TestAuthzLogStore_SaveReturnsMarshalError(t *testing.T) {
	database := setupAuthzLogSQLDB(t)
	store, err := sqlstore.NewAuthzLogStore(database, "authz_logs", gen.NewUUIDGenerator())
	require.NoError(t, err)

	err = store.SaveAuthzLogEntry(context.Background(), auth.AuthzLogEntry{
		ID:       "bad-json",
		Type:     auth.AuthzLogEntryTypeDecision,
		Metadata: map[string]any{"bad": func() {}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func setupAuthzLogSQLDB(t *testing.T) db.IDatabase {
	t.Helper()

	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)

	_, err = database.Exec(context.Background(), `
		CREATE TABLE authz_logs (
			id TEXT PRIMARY KEY,
			entry_type TEXT NOT NULL,
			decision_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			permission TEXT NOT NULL,
			operation TEXT NOT NULL,
			effect TEXT NOT NULL,
			reason_code TEXT NOT NULL,
			snapshot_version TEXT NOT NULL,
			snapshot_key TEXT NOT NULL,
			consistency TEXT NOT NULL,
			latency_ms INTEGER NOT NULL,
			cache_hit BOOLEAN NOT NULL,
			resources_json TEXT NOT NULL,
			execution_json TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
	`)
	require.NoError(t, err)
	return database
}
