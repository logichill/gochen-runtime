package sqlstore

import (
	"context"
	"fmt"
	"gochen-runtime/internal/testutil"
	"testing"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/eventing"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
)

type testEventPayload struct {
	Value int `json:"value"`
}

func newTestRegistry(tb testing.TB) *registry.Registry {
	tb.Helper()
	reg := registry.NewRegistry()
	require.NoError(tb, reg.Register("TestEvent", func() any { return &testEventPayload{} }))
	return reg
}

func newTestUpgraders() *upcast.UpgraderRegistry {
	return upcast.NewUpgraderRegistry()
}

func newTestEvent(aggregateID int64, version uint64, id string, payload map[string]any) eventing.Event[int64] {
	if payload == nil {
		payload = make(map[string]any)
	}

	evt := testutil.NewEvent[int64](aggregateID, "TestAggregate", "TestEvent", version, payload)
	evt.ID = id
	evt.GetMetadata().Set("source", "unit_test")
	return *evt
}

func setupTestDB(tb testing.TB) db.IDatabase {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(tb, err)

	setupOutboxTable(tb, database, "event_outbox")

	return database
}

func setupOutboxTable(tb testing.TB, database db.IDatabase, tableName string) {
	tb.Helper()

	_, err := database.Exec(context.Background(), fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			aggregate_id INTEGER NOT NULL,
			aggregate_type TEXT NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			event_type TEXT NOT NULL,
			event_data TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			claim_token TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			published_at DATETIME NULL,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NULL,
			lease_until DATETIME NULL,
			next_retry_at DATETIME NULL
		)
	`, tableName))
	require.NoError(tb, err)
}

func setupDLQTable(tb testing.TB, database db.IDatabase, tableName string) {
	tb.Helper()

	_, err := database.Exec(context.Background(), fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			original_entry_id INTEGER NOT NULL,
			aggregate_id INTEGER NOT NULL,
			aggregate_type TEXT NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			event_type TEXT NOT NULL,
			event_data TEXT NOT NULL,
			failure_reason TEXT NULL,
			retry_count INTEGER NOT NULL,
			moved_at DATETIME NOT NULL
		)
	`, tableName))
	require.NoError(tb, err)
}
