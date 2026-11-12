package sqlstore

import (
	"context"
	"testing"
	"time"

	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/eventing/projection"
	"gochen/testkit/require"
)

func TestCheckpointRunnerUsesSessionBoundEngine(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			database, repository, runner := newRunnerFixture(t, "runner-session-bound")
			session, err := runner.ormEngine.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Rollback() })
			boundRunner, err := NewSQLCheckpointTxRunner(session)
			require.NoError(t, err)
			calls := 0
			require.NoError(t, boundRunner.WithinTx(context.Background(), func(ctx context.Context) error {
				_, owned, ok := orm.TxSessionFromContext(ctx)
				require.True(t, ok)
				require.False(t, owned)
				if err := repository.Create(ctx, &runnerItem{ID: 1, Name: "inner"}, spec.All()); err != nil {
					return err
				}
				return contextx.AppendAfterCommit(ctx, func(context.Context) error { calls++; return nil })
			}))
			require.Equal(t, 0, calls)
			if commit {
				require.NoError(t, session.Commit())
				require.Equal(t, 1, calls)
				require.Equal(t, 1, countRunnerRows(t, database, 1))
			} else {
				require.NoError(t, session.Rollback())
				require.Equal(t, 0, calls)
				require.Equal(t, 0, countRunnerRows(t, database, 1))
			}
		})
	}
}

func TestCheckpointRunnerJoinsOuterRepositoryTransaction(t *testing.T) {
	for _, mode := range []string{"rollback", "swallowed", "success", "panic"} {
		t.Run(mode, func(t *testing.T) {
			database, repository, runner := newRunnerFixture(t, "runner-join-"+mode)
			calls := 0
			abort := errors.NewCode(errors.Internal, "abort")
			err := repository.WithinTx(context.Background(), func(outer context.Context) error {
				call := func() error {
					return runner.WithinTx(outer, func(inner context.Context) error {
						if err := repository.Create(inner, &runnerItem{ID: 1, Name: "inner"}, spec.All()); err != nil {
							return err
						}
						if err := contextx.AppendAfterCommit(inner, func(context.Context) error { calls++; return nil }); err != nil {
							return err
						}
						if mode == "swallowed" {
							return abort
						}
						if mode == "panic" {
							panic("abort")
						}
						return nil
					})
				}
				if mode == "panic" {
					func() { defer func() { require.Equal(t, "abort", recover()) }(); _ = call() }()
					return nil
				}
				innerErr := call()
				if mode == "swallowed" {
					require.True(t, errors.Is(innerErr, abort))
					return nil
				}
				if innerErr != nil {
					return innerErr
				}
				require.Equal(t, 0, calls)
				if mode == "rollback" {
					return abort
				}
				return nil
			})
			if mode == "success" {
				require.NoError(t, err)
				require.Equal(t, 1, countRunnerRows(t, database, 1))
				require.Equal(t, 1, calls)
			} else {
				require.Error(t, err)
				require.Equal(t, 0, countRunnerRows(t, database, 1))
				require.Equal(t, 0, calls)
			}
		})
	}
}

func TestCheckpointReadsAndDeleteUseContextTransaction(t *testing.T) {
	database, _, runner := newRunnerFixture(t, "checkpoint-context-crud")
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	ctx := context.Background()
	require.NoError(t, store.CreateTable(ctx))
	checkpoint := projection.NewCheckpoint("docs", 7, "event7", time.Now())
	require.NoError(t, runner.WithinTx(ctx, func(txCtx context.Context) error {
		if err := store.Save(txCtx, checkpoint); err != nil {
			return err
		}
		got, err := store.Load(txCtx, "docs")
		require.NoError(t, err)
		require.Equal(t, int64(7), got.Position)
		list, err := store.List(txCtx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		return nil
	}))
	abort := errors.NewCode(errors.Internal, "abort")
	err := runner.WithinTx(ctx, func(txCtx context.Context) error {
		if err := store.Delete(txCtx, "docs"); err != nil {
			return err
		}
		_, err := store.Load(txCtx, "docs")
		require.True(t, errors.Is(err, errors.NotFound))
		return abort
	})
	require.True(t, errors.Is(err, abort))
	_, err = store.Load(ctx, "docs")
	require.NoError(t, err)
	_, otherRepo, otherRunner := newRunnerFixture(t, "checkpoint-context-other")
	require.NoError(t, otherRunner.WithinTx(ctx, func(txCtx context.Context) error {
		require.True(t, errors.Is(store.Delete(txCtx, "docs"), errors.FailedPrecondition))
		_, err := store.Load(txCtx, "docs")
		require.True(t, errors.Is(err, errors.FailedPrecondition))
		_, err = store.List(txCtx)
		require.True(t, errors.Is(err, errors.FailedPrecondition))
		require.True(t, errors.Is(runner.WithinTx(txCtx, func(context.Context) error { t.Fatal("foreign session executed"); return nil }), errors.FailedPrecondition))
		return otherRepo.Create(txCtx, &runnerItem{ID: 1, Name: "other"}, spec.All())
	}))
}
