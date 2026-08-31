package migrate

import (
	"context"

	"gochen/db"
	"gochen/errors"
)

func (r *Runner) applyFile(ctx context.Context, dirtyVersion uint64, cleanVersion uint64, file File) error {
	content, err := r.source.Read(ctx, file)
	if err != nil {
		return err
	}
	if containsManualReviewGuard(string(content)) {
		return errors.NewCodeWithCause(errors.FailedPrecondition, "migration review required", ErrReviewRequired).
			WithContext("version", dirtyVersion).
			WithContext("path", file.Path)
	}
	statements := SplitStatements(string(content))
	if len(statements) == 0 {
		return errors.NewCode(errors.InvalidInput, "migration file is empty").
			WithContext("version", dirtyVersion).
			WithContext("path", file.Path)
	}

	if r.disableTx {
		return r.applyStatements(ctx, r.db, dirtyVersion, cleanVersion, file, statements, true)
	}
	if err := r.applyStatementsInTransaction(ctx, dirtyVersion, cleanVersion, file, statements); err == nil {
		return nil
	} else if !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	return r.applyStatements(ctx, r.db, dirtyVersion, cleanVersion, file, statements, true)
}

func (r *Runner) applyStatementsInTransaction(ctx context.Context, dirtyVersion uint64, cleanVersion uint64, file File, statements []string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return err
		}
		return errors.Wrap(err, errors.Database, "begin migration transaction failed").
			WithContext("version", dirtyVersion).
			WithContext("direction", file.Direction).
			WithContext("path", file.Path)
	}

	txClosed := false
	defer func() {
		if !txClosed {
			_ = tx.Rollback()
		}
	}()

	if err := r.applyStatements(ctx, tx, dirtyVersion, cleanVersion, file, statements, false); err != nil {
		_ = tx.Rollback()
		txClosed = true
		if dirtyErr := r.writeStatus(ctx, Status{Type: r.migrationType, Version: dirtyVersion, Dirty: true}); dirtyErr != nil {
			return errors.Join(err, dirtyErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		wrapped := errors.Wrap(err, errors.Database, "commit migration transaction failed").
			WithContext("version", dirtyVersion).
			WithContext("direction", file.Direction).
			WithContext("path", file.Path)
		if dirtyErr := r.writeStatus(ctx, Status{Type: r.migrationType, Version: dirtyVersion, Dirty: true}); dirtyErr != nil {
			return errors.Join(wrapped, dirtyErr)
		}
		return wrapped
	}
	txClosed = true
	return nil
}

func (r *Runner) applyStatements(ctx context.Context, database db.IDatabase, dirtyVersion uint64, cleanVersion uint64, file File, statements []string, refreshLock bool) error {
	if err := r.lockHeartbeatError(); err != nil {
		return err
	}
	if refreshLock {
		if err := r.refreshLock(ctx); err != nil {
			return err
		}
	}
	if err := r.writeStatusWithDBOptions(ctx, database, Status{Type: r.migrationType, Version: dirtyVersion, Dirty: true}, refreshLock); err != nil {
		return err
	}
	for i, statement := range statements {
		if err := r.lockHeartbeatError(); err != nil {
			return err
		}
		if refreshLock {
			if err := r.refreshLock(ctx); err != nil {
				return err
			}
		}
		if _, err := database.Exec(ctx, statement); err != nil {
			return errors.Wrap(err, errors.Database, "apply migration failed").
				WithContext("version", dirtyVersion).
				WithContext("direction", file.Direction).
				WithContext("path", file.Path).
				WithContext("statement_index", i+1)
		}
	}
	if refreshLock {
		if err := r.refreshLock(ctx); err != nil {
			return err
		}
	}
	if err := r.lockHeartbeatError(); err != nil {
		return err
	}
	return r.writeStatusWithDBOptions(ctx, database, Status{Type: r.migrationType, Version: cleanVersion, Dirty: false}, refreshLock)
}

func (r *Runner) applyUpTo(ctx context.Context, state Status, target uint64, limit int) error {
	migrations, err := r.list(ctx)
	if err != nil {
		return err
	}

	applied := 0
	for _, migration := range migrations {
		if migration.Version <= state.Version {
			continue
		}
		if target > 0 && migration.Version > target {
			break
		}
		if limit > 0 && applied >= limit {
			break
		}
		if migration.Up == nil {
			return errors.NewCode(errors.InvalidInput, "up migration file missing").
				WithContext("version", migration.Version).
				WithContext("name", migration.Name)
		}
		if err := r.applyFile(ctx, migration.Version, migration.Version, *migration.Up); err != nil {
			return err
		}
		state.Version = migration.Version
		applied++
	}
	if target > 0 && state.Version != target {
		return errors.NewCode(errors.NotFound, "target migration version not found").
			WithContext("version", target)
	}
	if applied == 0 {
		return ErrNoChange
	}
	return nil
}

func (r *Runner) applyDownTo(ctx context.Context, state Status, target uint64, limit int) error {
	migrations, err := r.list(ctx)
	if err != nil {
		return err
	}

	applied := 0
	for state.Version > target {
		if limit > 0 && applied >= limit {
			break
		}
		index := findMigrationIndex(migrations, state.Version)
		if index < 0 {
			return errors.NewCode(errors.NotFound, "current migration version not found in source").
				WithContext("version", state.Version)
		}
		migration := migrations[index]
		if migration.Down == nil {
			return errors.NewCode(errors.InvalidInput, "down migration file missing").
				WithContext("version", migration.Version).
				WithContext("name", migration.Name)
		}
		nextVersion := previousVersion(migrations, index)
		if err := r.applyFile(ctx, migration.Version, nextVersion, *migration.Down); err != nil {
			return err
		}
		state.Version = nextVersion
		applied++
	}
	if applied == 0 {
		return ErrNoChange
	}
	return nil
}

func (r *Runner) ensureTargetExists(ctx context.Context, target uint64) error {
	migrations, err := r.list(ctx)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.Version == target {
			return nil
		}
	}
	return errors.NewCode(errors.NotFound, "target migration version not found").
		WithContext("version", target)
}

func previousVersion(migrations []Migration, index int) uint64 {
	if index <= 0 {
		return 0
	}
	return migrations[index-1].Version
}

func findMigrationIndex(migrations []Migration, version uint64) int {
	lo, hi := 0, len(migrations)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if migrations[mid].Version < version {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(migrations) && migrations[lo].Version == version {
		return lo
	}
	return -1
}
