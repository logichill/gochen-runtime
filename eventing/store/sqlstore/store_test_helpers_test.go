package sqlstore

import (
	"context"
	"gochen-runtime/internal/testutil"
	"testing"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/eventing"
)

func setupTestDB(tb testing.TB) db.IDatabase {
	tb.Helper()

	// SQLite :memory: 数据库属于单条连接，限制连接数避免并发请求打开另一个空数据库。
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	require.NoError(tb, err)

	ctx := context.Background()
	_, err = database.Exec(ctx, `
        CREATE TABLE event_store (
            id TEXT PRIMARY KEY,
            type TEXT NOT NULL,
            aggregate_id INTEGER NOT NULL,
            aggregate_type TEXT NOT NULL,
            version INTEGER NOT NULL,
            schema_version INTEGER NOT NULL,
            timestamp DATETIME NOT NULL,
            payload TEXT NOT NULL,
            metadata TEXT NOT NULL,
            UNIQUE(aggregate_id, aggregate_type, version)
        );
    `)
	require.NoError(tb, err)

	return database
}

// makeEvent aggregateID：对象/实体标识。
//
// 参数：
// - aggregateType：聚合类型（类型：string）
// - id：对象/实体标识
// - version：版本号（类型：uint64）
// - payload：属性/参数集合（类型：map[string]any）
//
// 返回：
// - result：测试返回值（类型：eventing.Event[int64]）
func makeEvent(aggregateID int64, aggregateType, id string, version uint64, payload map[string]any) eventing.Event[int64] {
	if payload == nil {
		payload = make(map[string]any)
	}

	evt := testutil.NewEvent[int64](aggregateID, aggregateType, "TestEvent", version, payload)
	evt.ID = id
	return *evt
}

// toStorableEvents events：事件列表（待追加/发布）（类型：[]eventing.Event[int64]）。
//
// 参数：
//
// 返回：
// - result：列表结果（元素类型：eventing.IStorableEvent[int64]）
func toStorableEvents(events []eventing.Event[int64]) []eventing.IStorableEvent[int64] {
	storable := make([]eventing.IStorableEvent[int64], len(events))
	for i := range events {
		storable[i] = &events[i]
	}
	return storable
}
