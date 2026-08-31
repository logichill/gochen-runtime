package sqlstore

import (
	"context"
	stderrors "errors"
	"gochen-runtime/internal/testutil"
	"sync"
	"testing"
	"time"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/projection"
)

type checkpointingTxRunner struct {
	calls int
}

func (r *checkpointingTxRunner) WithinTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	r.calls++
	return fn(ctx)
}

func TestCheckpointingProjector_SQLCheckpointStoreRequiresOrmTxSession(t *testing.T) {
	projector := newMockProjection("sql-checkpoint-no-tx", []string{"TestEvent"})
	wrapped, err := projection.NewCheckpointingProjector[int64](projector, &checkpointingTxRunner{})
	require.NoError(t, err)

	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(context.Background()))

	evt := testutil.NewEvent[int64](1, "Agg", "TestEvent", 1, nil)
	err = wrapped.HandleWithCheckpoint(context.Background(), evt, store, projection.NewCheckpoint(wrapped.Name(), 1, evt.ID, evt.Timestamp))
	require.Error(t, err)
	require.Equal(t, errors.Unsupported, errors.Code(err))
}

type decoratedCheckpointStore struct {
	projection.ICheckpointStore
	requiresTx bool
}

func (s decoratedCheckpointStore) RequiresORMTxSession() bool {
	return s.requiresTx
}

func TestCheckpointingProjector_DecoratedSQLCheckpointStoreRequiresOrmTxSession(t *testing.T) {
	projector := newMockProjection("sql-checkpoint-decorated-no-tx", []string{"TestEvent"})
	wrapped, err := projection.NewCheckpointingProjector[int64](projector, &checkpointingTxRunner{})
	require.NoError(t, err)

	database := newProjectionCheckpointTestDB(t)
	baseStore := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, baseStore.CreateTable(context.Background()))

	store := decoratedCheckpointStore{
		ICheckpointStore: baseStore,
		requiresTx:       true,
	}
	evt := testutil.NewEvent[int64](1, "Agg", "TestEvent", 1, nil)
	err = wrapped.HandleWithCheckpoint(context.Background(), evt, store, projection.NewCheckpoint(wrapped.Name(), 1, evt.ID, evt.Timestamp))
	require.Error(t, err)
	require.Equal(t, errors.Unsupported, errors.Code(err))
}

func TestCheckpointingProjector_SQLCheckpointFailureRollsBackReadModel(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	ormEngine := newProjectionCheckpointTestOrm(t, database)
	txRunner, err := NewSQLCheckpointTxRunner(ormEngine)
	require.NoError(t, err)

	projector := newMockProjection("sql-checkpoint-rollback", []string{"TestEvent"})
	projector.handleFunc = func(ctx context.Context, event eventing.IEvent) error {
		session, ok := orm.SessionFromContext(ctx)
		require.True(t, ok)
		_, err := session.Database().Exec(ctx, `INSERT INTO projection_read_model (id, value) VALUES (?, ?)`, event.GetID(), "written")
		return err
	}
	wrapped, err := projection.NewCheckpointingProjector[int64](projector, txRunner)
	require.NoError(t, err)

	store := NewSQLCheckpointStore(database, "missing_projection_checkpoints")
	evt := testutil.NewEvent[int64](1, "Agg", "TestEvent", 1, nil)
	err = wrapped.HandleWithCheckpoint(ctx, evt, store, projection.NewCheckpoint(wrapped.Name(), 1, evt.ID, evt.Timestamp))
	require.Error(t, err)
	require.Zero(t, projectionReadModelCount(t, database))
}

func TestCheckpointingProjector_ReadModelFailureDoesNotSaveSQLCheckpoint(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	ormEngine := newProjectionCheckpointTestOrm(t, database)
	txRunner, err := NewSQLCheckpointTxRunner(ormEngine)
	require.NoError(t, err)

	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	wantErr := stderrors.New("read model failed")
	projector := newMockProjection("sql-checkpoint-read-fail", []string{"TestEvent"})
	projector.handleFunc = func(ctx context.Context, event eventing.IEvent) error {
		return wantErr
	}
	wrapped, err := projection.NewCheckpointingProjector[int64](projector, txRunner)
	require.NoError(t, err)

	evt := testutil.NewEvent[int64](1, "Agg", "TestEvent", 1, nil)
	err = wrapped.HandleWithCheckpoint(ctx, evt, store, projection.NewCheckpoint(wrapped.Name(), 1, evt.ID, time.Now()))
	require.ErrorIs(t, err, wantErr)

	_, err = store.Load(ctx, wrapped.Name())
	require.Equal(t, errors.NotFound, errors.Code(err))
}

func TestTenantAwareProjector_SkippedEventSavesSQLCheckpointWithinTx(t *testing.T) {
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	database := newProjectionCheckpointTestDB(t)
	ormEngine := newProjectionCheckpointTestOrm(t, database)
	txRunner, err := NewSQLCheckpointTxRunner(ormEngine)
	require.NoError(t, err)

	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	projector := newMockProjection("tenant-skip-sql-checkpoint", []string{"TestEvent"})
	projector.handleFunc = func(context.Context, eventing.IEvent) error {
		t.Fatalf("tenant-mismatched event should not reach inner projection")
		return nil
	}
	wrapped, err := projection.NewCheckpointingProjector[int64](projector, txRunner)
	require.NoError(t, err)
	tenantWrapped := projection.NewTenantAwareProjector[int64](wrapped).(projection.ICheckpointingProjection[int64])

	evt := testutil.NewEvent[int64](1, "Agg", "TestEvent", 1, nil)
	evt.GetMetadata().Set(contextx.MetadataTenantKey, "tenant-b")

	err = tenantWrapped.HandleWithCheckpoint(ctx, evt, store, projection.NewCheckpoint(tenantWrapped.Name(), 1, evt.ID, evt.Timestamp))
	require.NoError(t, err)

	checkpoint, err := store.Load(ctx, tenantWrapped.Name())
	require.NoError(t, err)
	require.Equal(t, int64(1), checkpoint.Position)
	require.Equal(t, evt.ID, checkpoint.LastEventID)
}

func projectionReadModelCount(tb testing.TB, database db.IDatabase) int {
	tb.Helper()

	var count int
	require.NoError(tb, database.QueryRow(context.Background(), `SELECT COUNT(1) FROM projection_read_model`).Scan(&count))
	return count
}

func newProjectionCheckpointTestOrm(tb testing.TB, database db.IDatabase) orm.IOrm {
	tb.Helper()

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)
	return ormEngine
}

type mockProjection struct {
	name           string
	supportedTypes []string
	handleFunc     func(ctx context.Context, event eventing.IEvent) error
	mu             sync.Mutex
}

func newMockProjection(name string, types []string) *mockProjection {
	return &mockProjection{name: name, supportedTypes: types}
}

func (p *mockProjection) Name() string { return p.name }

func (p *mockProjection) Handle(ctx context.Context, event eventing.IEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handleFunc != nil {
		return p.handleFunc(ctx, event)
	}
	return nil
}

func (p *mockProjection) SupportedEventTypes() []string {
	return append([]string(nil), p.supportedTypes...)
}

func (p *mockProjection) Rebuild(ctx context.Context, events []eventing.Event[int64]) error {
	return nil
}

func (p *mockProjection) Status() projection.ProjectionStatus {
	return projection.ProjectionStatus{Status: "running"}
}
