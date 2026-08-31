package repo

import (
	"context"
	"database/sql"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

type txLifecycleEntity struct {
	ID      int64
	Version uint64
}

func (e *txLifecycleEntity) GetID() int64       { return e.ID }
func (e *txLifecycleEntity) GetVersion() uint64 { return e.Version }

func newSessionBoundRepo(tb testing.TB) (*Repo[*txLifecycleEntity, int64], orm.IOrmSession) {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)
	session, err := ormEngine.BeginTx(context.Background(), nil)
	require.NoError(tb, err)

	repository, err := NewRepo[*txLifecycleEntity, int64](session, "tx_entities")
	require.NoError(tb, err)
	return repository, session
}

func TestRepoBeginTx_SessionBoundRepoRunsPostCommitOnManualCommit(t *testing.T) {
	repository, session := newSessionBoundRepo(t)

	txCtx, err := repository.beginTx(context.Background())
	require.NoError(t, err)

	called := 0
	require.NoError(t, contextx.AppendAfterCommit(txCtx.Context(), func(context.Context) error {
		called++
		return nil
	}))

	require.NoError(t, session.Commit())
	require.Equal(t, 1, called)
}

func TestRepoBeginTx_SessionBoundRepoWrapsAfterCommitError(t *testing.T) {
	repository, session := newSessionBoundRepo(t)

	txCtx, err := repository.beginTx(context.Background())
	require.NoError(t, err)

	want := errors.NewCode(errors.Internal, "callback failed")
	require.NoError(t, contextx.AppendAfterCommit(txCtx.Context(), func(context.Context) error {
		return want
	}))

	err = session.Commit()
	require.ErrorIs(t, err, want)
	require.True(t, contextx.IsAfterCommitError(err))
}

type unsupportedSessionModel struct {
	meta *orm.ModelMeta
}

func (m *unsupportedSessionModel) Meta() *orm.ModelMeta                                 { return m.meta }
func (m *unsupportedSessionModel) Capabilities() orm.Capabilities                       { return nil }
func (m *unsupportedSessionModel) Dialect() repoTestDialect                             { return unknownRepoTestDialect() }
func (m *unsupportedSessionModel) First(context.Context, any, ...orm.QueryOption) error { return nil }
func (m *unsupportedSessionModel) Find(context.Context, any, ...orm.QueryOption) error  { return nil }
func (m *unsupportedSessionModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return 0, nil
}
func (m *unsupportedSessionModel) Create(context.Context, ...any) error { return nil }
func (m *unsupportedSessionModel) Save(context.Context, any, ...orm.QueryOption) error {
	return nil
}
func (m *unsupportedSessionModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	return nil
}
func (m *unsupportedSessionModel) Delete(context.Context, ...orm.QueryOption) error { return nil }
func (m *unsupportedSessionModel) Association(any, string) orm.IAssociation         { return nil }

type unsupportedSession struct {
	model *unsupportedSessionModel
}

func (s *unsupportedSession) Capabilities() orm.Capabilities { return nil }
func (s *unsupportedSession) WithContext(context.Context) orm.IOrm {
	return s
}
func (s *unsupportedSession) Model(meta *orm.ModelMeta) (orm.IModel, error) {
	if s.model == nil {
		s.model = &unsupportedSessionModel{}
	}
	s.model.meta = meta
	return s.model, nil
}
func (s *unsupportedSession) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *unsupportedSession) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *unsupportedSession) Database() db.IDatabase { return nil }
func (s *unsupportedSession) Commit() error          { return nil }
func (s *unsupportedSession) Rollback() error        { return nil }

func TestNewRepo_SessionBoundRepoWithoutDispatcherFailsFast(t *testing.T) {
	session := &unsupportedSession{}

	_, err := NewRepo[*txLifecycleEntity, int64](session, "tx_entities")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Unsupported))
}

type constrainedBatchTestOrm struct {
	session *constrainedBatchTestSession
}

type constrainedBatchTestSession struct {
	dispatcher    contextx.IAfterCommitDispatcher
	commitErr     error
	commitCalls   int
	rollbackCalls int
}

func (o *constrainedBatchTestOrm) Capabilities() orm.Capabilities       { return nil }
func (o *constrainedBatchTestOrm) WithContext(context.Context) orm.IOrm { return o }
func (o *constrainedBatchTestOrm) Model(meta *orm.ModelMeta) (orm.IModel, error) {
	return &unsupportedSessionModel{meta: meta}, nil
}
func (o *constrainedBatchTestOrm) Begin(context.Context) (orm.IOrmSession, error) {
	return o.session, nil
}
func (o *constrainedBatchTestOrm) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return o.session, nil
}
func (o *constrainedBatchTestOrm) Database() db.IDatabase { return nil }

func (s *constrainedBatchTestSession) Capabilities() orm.Capabilities       { return nil }
func (s *constrainedBatchTestSession) WithContext(context.Context) orm.IOrm { return s }
func (s *constrainedBatchTestSession) Model(meta *orm.ModelMeta) (orm.IModel, error) {
	return &unsupportedSessionModel{meta: meta}, nil
}
func (s *constrainedBatchTestSession) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *constrainedBatchTestSession) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *constrainedBatchTestSession) Database() db.IDatabase { return nil }
func (s *constrainedBatchTestSession) Commit() error {
	s.commitCalls++
	if s.commitErr != nil {
		return s.commitErr
	}
	if s.dispatcher != nil {
		if err := s.dispatcher.RunAfterCommit(); err != nil {
			return contextx.WrapAfterCommitError(err)
		}
	}
	return nil
}
func (s *constrainedBatchTestSession) Rollback() error {
	s.rollbackCalls++
	return nil
}
func (s *constrainedBatchTestSession) AfterCommitDispatcher() contextx.IAfterCommitDispatcher {
	if s.dispatcher == nil {
		s.dispatcher = contextx.NewAfterCommitDispatcher()
	}
	return s.dispatcher
}

func TestRepoConstrainedBatchTx_RollsBackOnCommitError(t *testing.T) {
	want := errors.New("commit failed")
	session := &constrainedBatchTestSession{commitErr: want}
	repository := &Repo[*txLifecycleEntity, int64]{orm: &constrainedBatchTestOrm{session: session}}

	err := repository.withConstrainedBatchTx(context.Background(), func(txCtx context.Context) error {
		return nil
	})

	require.ErrorIs(t, err, want)
	require.Equal(t, 1, session.commitCalls)
	require.Equal(t, 1, session.rollbackCalls)
}

func TestRepoConstrainedBatchTx_DoesNotRollbackOnAfterCommitError(t *testing.T) {
	want := errors.New("post commit failed")
	session := &constrainedBatchTestSession{}
	repository := &Repo[*txLifecycleEntity, int64]{orm: &constrainedBatchTestOrm{session: session}}

	err := repository.withConstrainedBatchTx(context.Background(), func(txCtx context.Context) error {
		return contextx.AppendAfterCommit(txCtx, func(context.Context) error {
			return want
		})
	})

	require.ErrorIs(t, err, want)
	require.True(t, contextx.IsAfterCommitError(err))
	require.Equal(t, 1, session.commitCalls)
	require.Equal(t, 0, session.rollbackCalls)
}
