package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
)

type archiveFailureDB struct {
	db.IDatabase
	mode string
	tx   *archiveFailureTx
}

func (d *archiveFailureDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	tx, err := d.IDatabase.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	d.tx = &archiveFailureTx{ITransaction: tx, mode: d.mode}
	return d.tx, nil
}

type archiveFailureTx struct {
	db.ITransaction
	mode      string
	rollbacks int
}

func (tx *archiveFailureTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := tx.ITransaction.Exec(ctx, query, args...)
	if err == nil && tx.mode == "panic" && strings.HasPrefix(strings.TrimSpace(query), "DELETE") {
		panic("archive delete panic")
	}
	return result, err
}

func (tx *archiveFailureTx) Commit() error {
	if tx.mode == "commit_failure" {
		return errors.New("archive commit failed")
	}
	return tx.ITransaction.Commit()
}

func (tx *archiveFailureTx) Rollback() error {
	tx.rollbacks++
	return tx.ITransaction.Rollback()
}

func TestCleanupService_ArchiveRollsBackOnPanicAndCommitFailure(t *testing.T) {
	for _, mode := range []string{"panic", "commit_failure"} {
		t.Run(mode, func(t *testing.T) {
			database := setupTestDB(t)
			t.Cleanup(func() { _ = database.Close() })
			ctx := context.Background()
			old := time.Now().Add(-48 * time.Hour)
			_, err := database.Exec(ctx, `INSERT INTO event_outbox
				(id, aggregate_id, aggregate_type, event_id, event_type, event_data, status, created_at, published_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, 1, 1, "Agg", "event-1", "TestEvent", "{}", outbox.OutboxStatusPublished, old, old)
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewCleanupService(database, CleanupPolicy{RetentionDays: 1, BatchSize: 10, ArchiveEnabled: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			failDB := &archiveFailureDB{IDatabase: database, mode: mode}
			service.db = failDB
			defer func() {
				if failDB.tx != nil {
					_ = failDB.tx.ITransaction.Rollback()
				}
			}()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_, err = service.Cleanup(ctx)
			}()
			if mode == "panic" && recovered != "archive delete panic" {
				t.Fatalf("panic = %v, want archive delete panic", recovered)
			}
			if mode == "commit_failure" && (recovered != nil || err == nil) {
				t.Fatalf("commit failure: panic=%v err=%v", recovered, err)
			}
			if failDB.tx == nil || failDB.tx.rollbacks != 1 {
				t.Fatalf("failed archive transaction was not rolled back: %+v", failDB.tx)
			}
			var sourceCount, archiveCount int
			if err := database.QueryRow(ctx, "SELECT COUNT(*) FROM event_outbox").Scan(&sourceCount); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(ctx, "SELECT COUNT(*) FROM event_outbox_archive").Scan(&archiveCount); err != nil {
				t.Fatal(err)
			}
			if sourceCount != 1 || archiveCount != 0 {
				t.Fatalf("archive rollback: source=%d archive=%d", sourceCount, archiveCount)
			}
		})
	}
}
