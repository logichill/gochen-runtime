package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gochen/db"
	"gochen/errors"
)

func (r *Runner) status(ctx context.Context) (Status, error) {
	ctx = normalizeContext(ctx)
	return r.readStatus(ctx)
}

func (r *Runner) ensureStateTable(ctx context.Context) error {
	table := r.quotedStateTable()
	query := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  migration_type VARCHAR(64) PRIMARY KEY,
  version BIGINT NOT NULL DEFAULT 0,
  dirty INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL
)`, table)
	if _, err := r.db.Exec(ctx, query); err != nil {
		return errors.Wrap(err, errors.Database, "ensure migration state table failed").
			WithContext("table", r.stateTable)
	}
	columns, err := r.stateColumns(ctx)
	if err != nil {
		return err
	}
	return validateStateTableColumns(r.stateTable, columns)
}

func (r *Runner) readStatus(ctx context.Context) (Status, error) {
	query := fmt.Sprintf("SELECT version, dirty FROM %s WHERE migration_type = ?", r.quotedStateTable())
	row := r.db.QueryRow(ctx, query, r.migrationType)
	var (
		version uint64
		dirty   int
	)
	err := row.Scan(&version, &dirty)
	if err == nil {
		return Status{Type: r.migrationType, Version: version, Dirty: dirty != 0}, nil
	}
	if err == sql.ErrNoRows {
		return Status{Type: r.migrationType}, nil
	}
	if isMissingStateTableError(err, r.stateTable) {
		return Status{Type: r.migrationType}, nil
	}
	return Status{}, errors.Wrap(err, errors.Database, "read migration status failed").
		WithContext("table", r.stateTable)
}

func (r *Runner) writeStatus(ctx context.Context, status Status) error {
	return r.writeStatusWithDB(ctx, r.db, status)
}

func (r *Runner) writeStatusWithDB(ctx context.Context, database db.IDatabase, status Status) error {
	return r.writeStatusWithDBOptions(ctx, database, status, true)
}

func (r *Runner) writeStatusWithDBOptions(ctx context.Context, database db.IDatabase, status Status, refreshLock bool) error {
	if refreshLock {
		if err := r.refreshLockWithDB(ctx, database); err != nil {
			return err
		}
	}
	migrationType := strings.TrimSpace(status.Type)
	if migrationType == "" {
		migrationType = r.migrationType
	}
	now := r.now().UTC()
	if handled, err := r.writeStatusWithUpsert(ctx, database, migrationType, status, now); handled {
		return err
	}
	return r.writeCompatStatusWithDB(ctx, database, migrationType, status, now)
}

func (r *Runner) writeStatusWithUpsert(ctx context.Context, database db.IDatabase, migrationType string, status Status, now time.Time) (bool, error) {
	query, ok := r.dialect.MigrationStateUpsertSQL(r.quotedStateTable())
	if !ok {
		return false, nil
	}
	if _, err := database.Exec(ctx, query, migrationType, status.Version, boolToInt(status.Dirty), now); err != nil {
		return true, errors.Wrap(err, errors.Database, "upsert migration status failed").
			WithContext("table", r.stateTable)
	}
	return true, nil
}

func (r *Runner) writeCompatStatusWithDB(ctx context.Context, database db.IDatabase, migrationType string, status Status, now time.Time) error {
	query := fmt.Sprintf("SELECT COUNT(1) FROM %s WHERE migration_type = ?", r.quotedStateTable())
	row := database.QueryRow(ctx, query, migrationType)
	var count int
	if err := row.Scan(&count); err != nil {
		return errors.Wrap(err, errors.Database, "read migration state existence failed").
			WithContext("table", r.stateTable)
	}
	if count == 0 {
		insert := fmt.Sprintf(
			"INSERT INTO %s (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?)",
			r.quotedStateTable(),
		)
		if _, err := database.Exec(ctx, insert, migrationType, status.Version, boolToInt(status.Dirty), now); err != nil {
			if db.IsUniqueViolation(err) {
				return r.updateStatusRow(ctx, database, migrationType, status, now)
			}
			return errors.Wrap(err, errors.Database, "insert migration status failed").
				WithContext("table", r.stateTable)
		}
		return nil
	}
	return r.updateStatusRow(ctx, database, migrationType, status, now)
}

func (r *Runner) updateStatusRow(ctx context.Context, database db.IDatabase, migrationType string, status Status, now time.Time) error {
	update := fmt.Sprintf(
		"UPDATE %s SET version = ?, dirty = ?, updated_at = ? WHERE migration_type = ?",
		r.quotedStateTable(),
	)
	if _, err := database.Exec(ctx, update, status.Version, boolToInt(status.Dirty), now, migrationType); err != nil {
		return errors.Wrap(err, errors.Database, "update migration status failed").
			WithContext("table", r.stateTable)
	}
	return nil
}

func (r *Runner) stateColumns(ctx context.Context) (map[string]bool, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", r.quotedStateTable())
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "inspect migration state table columns failed").
			WithContext("table", r.stateTable)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "inspect migration state table columns failed").
			WithContext("table", r.stateTable)
	}
	out := make(map[string]bool, len(columns))
	for _, column := range columns {
		out[strings.ToLower(strings.TrimSpace(column))] = true
	}
	return out, nil
}

func validateStateTableColumns(table string, columns map[string]bool) error {
	for _, column := range []string{"migration_type", "version", "dirty", "updated_at"} {
		if columns[column] {
			continue
		}
		return errors.NewCode(errors.InvalidInput, "migration state table is not gochen format").
			WithContext("table", table).
			WithContext("missing_column", column)
	}
	return nil
}
