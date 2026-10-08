package sqlstore

import (
	"testing"

	"gochen/eventing/store"
	"gochen/eventing/store/contracttest"
)

func TestSQLEventStoreContract(t *testing.T) {
	for _, mode := range []string{"timestamp", "global_position"} {
		t.Run(mode, func(t *testing.T) { runSQLContract(t, mode == "global_position") })
	}
}

func runSQLContract(t *testing.T, globalPosition bool) {
	newStore := func(t *testing.T) *SQLEventStore[int64] {
		database := setupTestDB(t)
		t.Cleanup(func() {
			if err := database.Close(); err != nil {
				t.Error(err)
			}
		})
		if globalPosition {
			addGlobalPositionColumnAndIndex(t, database)
		}
		return newTestStore(t, database, "event_store")
	}
	contracttest.RunEventStore(t, func(t *testing.T) store.IEventStore[int64] { return newStore(t) })
	contracttest.RunEventStream(t, func(t *testing.T) store.IEventStreamStore[int64] { return newStore(t) })
	contracttest.RunAtomic(t, func(t *testing.T) contracttest.IAtomicBackend { return newStore(t) })
}
