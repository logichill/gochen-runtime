package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"gochen/db"
	"gochen/errors"
)

type delayedHeartbeatDB struct {
	db.IDatabase
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (d *delayedHeartbeatDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if isLockRefreshQuery(query, args...) && d.blocked.CompareAndSwap(false, true) {
		close(d.entered)
		<-d.release
		return nil, errors.New("previous lock heartbeat connection failed")
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

// 心跳超过停止等待上限后仍属于旧锁，不能污染同一 Runner 后来获取的新锁。
func TestPreviousHeartbeatFailureDoesNotPoisonReacquiredLock(t *testing.T) {
	source, err := NewFS(fstest.MapFS{"000001_create_items.up.sql": {Data: []byte("CREATE TABLE items (id INTEGER PRIMARY KEY);")}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	database := &delayedHeartbeatDB{IDatabase: openSQLiteDB(t, filepath.Join(t.TempDir(), "runner.db")), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(database.release) }) }
	t.Cleanup(release)
	runner, err := NewRunner(database, source, WithLockStaleAfter(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var oldDone chan struct{}
	if err := runner.WithLock(t.Context(), func() error {
		oldDone = runner.lockDone
		select {
		case <-database.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("first heartbeat did not start")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// releaseLock 已等待五秒并释放旧锁，旧心跳仍阻塞在数据库调用。
	err = runner.WithLock(t.Context(), func() error {
		release()
		select {
		case <-oldDone:
		case <-time.After(time.Second):
			t.Fatal("old heartbeat did not finish")
		}
		return runner.refreshLock(t.Context())
	})
	if err != nil {
		t.Fatalf("new lock inherited old heartbeat error: %v", err)
	}
}
