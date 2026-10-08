package sqlstore

import (
	"fmt"
	"testing"
	"time"

	"gochen/observe/logging"
)

func TestArchiveConflictDoesNotStarveFollowingRows(t *testing.T) {
	for _, timestampFormat := range []string{"driver", time.DateTime, time.RFC3339Nano} {
		for _, batchSize := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/%d", timestampFormat, batchSize), func(t *testing.T) {
				database := setupTestDB(t)
				ctx := t.Context()
				service, err := NewCleanupService(database, CleanupPolicy{RetentionDays: 1, BatchSize: batchSize, ArchiveEnabled: true}, logging.NewNoopLogger())
				if err != nil {
					t.Fatal(err)
				}
				old := time.Now().UTC().AddDate(0, 0, -10)
				insert := func(id int, eventID string, created time.Time) {
					t.Helper()
					var timestamp any = created
					if timestampFormat != "driver" {
						timestamp = created.Format(timestampFormat)
					}
					_, err := database.Exec(ctx, `INSERT INTO event_outbox (id, aggregate_id, aggregate_type, event_id, event_type, event_data, status, created_at, published_at) VALUES (?,1,'A',?,'E','{}','published',?,?)`, id, eventID, timestamp, timestamp)
					if err != nil {
						t.Fatal(err)
					}
				}
				insert(10, "archived-10", old)
				insert(20, "archived-20", old)
				if _, err := service.Cleanup(ctx); err != nil {
					t.Fatal(err)
				}
				insert(10, "conflict-10", old)
				insert(20, "conflict-20", old)
				// 同时间戳按 ID 前进；较晚时间戳的较小 ID 也必须被扫描。
				insert(30, "normal-30", old)
				insert(1, "normal-1", old.Add(time.Hour))
				result, err := service.Cleanup(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if result.ArchivedCount != 2 {
					t.Fatalf("archived=%d, want both nonconflicting rows", result.ArchivedCount)
				}
				for _, table := range []string{"event_outbox", "event_outbox_archive"} {
					var count int
					if err := database.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
						t.Fatal(err)
					}
					want := 2
					if table == "event_outbox_archive" {
						want = 4
					}
					if count != want {
						t.Fatalf("%s has %d rows, want %d", table, count, want)
					}
				}
				result, err = service.Cleanup(ctx)
				if err != nil || result.ArchivedCount != 0 {
					t.Fatalf("repeated cleanup: result=%+v, err=%v", result, err)
				}
			})
		}
	}
}
