package main

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	outboxsql "gochen-runtime/eventing/outbox/sqlstore"
	storesql "gochen-runtime/eventing/store/sqlstore"
	"gochen-runtime/examples/infra/outbox/sql/internal/schema"
	"gochen/db"
	"gochen/observe/logging"
)

func TestConfigureMonitoringWiresOutboxProvider(t *testing.T) {
	ctx := context.Background()
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer func() { _ = database.Close() }()
	if err := schema.EnsureEventAndOutboxTables(ctx, database); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}

	eventStore, err := storesql.NewSQLEventStore(database, "event_store", storesql.WithLogger(logging.NewNoopLogger()))
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	repo, err := outboxsql.NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	if err != nil {
		t.Fatalf("new outbox repo: %v", err)
	}

	reg, err := configureMonitoring(database, repo)
	if err != nil {
		t.Fatalf("configure monitoring: %v", err)
	}
	if reg == nil {
		t.Fatal("expected monitoring registry")
	}
	snapshot := reg.Snapshot(ctx)
	if snapshot.Outbox == nil {
		t.Fatal("expected outbox snapshot")
	}
	if snapshot.Outbox.Error != "" {
		t.Fatalf("outbox snapshot error: %s", snapshot.Outbox.Error)
	}
}
