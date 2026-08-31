package sqlstore

import (
	"context"
	"encoding/json"
	"testing"

	"gochen/testkit/require"

	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/messaging"
)

type typedPayload struct {
	Amount int `json:"amount"`
}

// TestSQLEventStore_LoadEvents_DefaultPayloadIsRawMessageAndCanHydrateToTypedPayload 验证 SQLEventStore LoadEvents DefaultPayloadIsRawMessageAndCanHydrateToTypedPayload。
func TestSQLEventStore_LoadEvents_DefaultPayloadIsRawMessageAndCanHydrateToTypedPayload(t *testing.T) {
	db := setupTestDB(t)
	store := newTestStore(t, db, "event_store")

	// 注册事件类型（schema=1），并在事件中写入 schema_version=1
	reg := registry.NewRegistry()
	upgraders := upcast.NewUpgraderRegistry()
	require.NoError(t, reg.RegisterWithVersion("TypedEvent", 1, func() any { return &typedPayload{} }))

	ctx := context.Background()
	_, err := db.Exec(ctx, `
        INSERT INTO event_store (id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata)
        VALUES ('e-1', 'TypedEvent', 1, 'Agg', 1, 1, CURRENT_TIMESTAMP, '{"amount": 7}', '{}')
    `)
	require.NoError(t, err)

	evts, err := store.LoadEvents(ctx, "Agg", 1, 0)
	require.NoError(t, err)
	require.Len(t, evts, 1)

	_, ok := messaging.PayloadAs[json.RawMessage](evts[0].Payload)
	require.True(t, ok)

	_, err = upcast.UpgradeEventPayload(ctx, reg, upgraders, &evts[0])
	require.NoError(t, err)

	p, ok := messaging.PayloadAs[*typedPayload](evts[0].Payload)
	require.True(t, ok)
	require.Equal(t, 7, p.Amount)
}
