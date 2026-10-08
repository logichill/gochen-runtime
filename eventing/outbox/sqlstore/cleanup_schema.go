package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	coredialect "gochen/db/dialect"
	"strings"

	"gochen/errors"
)

func (s *CleanupService) ensureArchiveTable(ctx context.Context) error {
	if err := s.createArchiveTable(ctx); err != nil {
		return err
	}

	columns, err := s.archiveTableColumns(ctx)
	if err != nil {
		return err
	}

	type archiveColumn struct {
		name       string
		definition string
	}
	required := []archiveColumn{
		{name: "claim_token", definition: archiveClaimTokenDefinition(s.dialect)},
		{name: "lease_until", definition: archiveLeaseUntilDefinition(s.dialect)},
		{name: "next_retry_at", definition: archiveNextRetryAtDefinition(s.dialect)},
	}
	for _, column := range required {
		if _, ok := columns[column.name]; ok {
			continue
		}
		query := fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN %s %s",
			s.dialect.QuoteIdentifier(s.policy.ArchiveTable),
			s.dialect.QuoteIdentifier(column.name),
			column.definition,
		)
		if _, err := s.db.Exec(ctx, query); err != nil {
			return errors.Wrap(err, errors.Dependency, "ensure archive table column failed").
				WithContext("table", s.policy.ArchiveTable).
				WithContext("column", column.name)
		}
	}
	return nil
}

func (s *CleanupService) createArchiveTable(ctx context.Context) error {
	quotedTable := s.dialect.QuoteIdentifier(s.policy.ArchiveTable)
	var query string
	switch s.dialect.Name() {
	case coredialect.NameSQLite:
		query = fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id INTEGER PRIMARY KEY,
				aggregate_id INTEGER NOT NULL,
				aggregate_type TEXT NOT NULL,
				event_id TEXT NOT NULL UNIQUE,
				event_type TEXT NOT NULL,
				event_data TEXT NOT NULL,
				status TEXT NOT NULL,
				claim_token TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				published_at DATETIME NULL,
				retry_count INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NULL,
				lease_until DATETIME NULL,
				next_retry_at DATETIME NULL
			)
		`, quotedTable)
	case coredialect.NamePostgres:
		query = fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT PRIMARY KEY,
				aggregate_id BIGINT NOT NULL,
				aggregate_type VARCHAR(255) NOT NULL,
				event_id VARCHAR(255) NOT NULL UNIQUE,
				event_type VARCHAR(255) NOT NULL,
				event_data TEXT NOT NULL,
				status VARCHAR(32) NOT NULL,
				claim_token VARCHAR(255) NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL,
				published_at TIMESTAMPTZ NULL,
				retry_count INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NULL,
				lease_until TIMESTAMPTZ NULL,
				next_retry_at TIMESTAMPTZ NULL
			)
		`, quotedTable)
	default:
		query = fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT PRIMARY KEY,
				aggregate_id BIGINT NOT NULL,
				aggregate_type VARCHAR(255) NOT NULL,
				event_id VARCHAR(255) NOT NULL UNIQUE,
				event_type VARCHAR(255) NOT NULL,
				event_data TEXT NOT NULL,
				status VARCHAR(32) NOT NULL,
				claim_token VARCHAR(255) NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL,
				published_at DATETIME NULL,
				retry_count INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NULL,
				lease_until DATETIME NULL,
				next_retry_at DATETIME NULL
			)
		`, quotedTable)
	}

	if _, err := s.db.Exec(ctx, query); err != nil {
		return errors.Wrap(err, errors.Dependency, "create archive table failed").
			WithContext("table", s.policy.ArchiveTable)
	}
	return nil
}

func (s *CleanupService) archiveTableColumns(ctx context.Context) (map[string]struct{}, error) {
	schemaName, tableName := splitQualifiedTableName(s.policy.ArchiveTable)
	columns := make(map[string]struct{})

	switch s.dialect.Name() {
	case coredialect.NameSQLite:
		query := fmt.Sprintf("PRAGMA table_info(%s)", s.dialect.QuoteIdentifier(tableName))
		if schemaName != "" {
			query = fmt.Sprintf(
				"PRAGMA %s.table_info(%s)",
				s.dialect.QuoteIdentifier(schemaName),
				s.dialect.QuoteIdentifier(tableName),
			)
		}
		rows, err := s.db.Query(ctx, query)
		if err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "query archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
		defer rows.Close()

		for rows.Next() {
			var cid, notNull, pk int
			var name, columnTyp string
			var defaultV sql.NullString
			if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
				return nil, errors.Wrap(err, errors.Dependency, "scan archive table columns failed").WithContext("table", s.policy.ArchiveTable)
			}
			columns[strings.ToLower(name)] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "iterate archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
	case coredialect.NamePostgres:
		query := "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?"
		args := []any{tableName}
		if schemaName != "" {
			query = "SELECT column_name FROM information_schema.columns WHERE table_schema = ? AND table_name = ?"
			args = []any{schemaName, tableName}
		}
		rows, err := s.db.Query(ctx, s.dialect.Rebind(query), args...)
		if err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "query archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, errors.Wrap(err, errors.Dependency, "scan archive table columns failed").WithContext("table", s.policy.ArchiveTable)
			}
			columns[strings.ToLower(name)] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "iterate archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
	default:
		query := "SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?"
		args := []any{tableName}
		if schemaName != "" {
			query = "SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?"
			args = []any{schemaName, tableName}
		}
		rows, err := s.db.Query(ctx, s.dialect.Rebind(query), args...)
		if err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "query archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, errors.Wrap(err, errors.Dependency, "scan archive table columns failed").WithContext("table", s.policy.ArchiveTable)
			}
			columns[strings.ToLower(name)] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "iterate archive table columns failed").WithContext("table", s.policy.ArchiveTable)
		}
	}

	return columns, nil
}

func archiveClaimTokenDefinition(d coredialect.IDialect) string {
	if d.Name() == coredialect.NameSQLite {
		return "TEXT NOT NULL DEFAULT ''"
	}
	return "VARCHAR(255) NOT NULL DEFAULT ''"
}

func archiveLeaseUntilDefinition(d coredialect.IDialect) string {
	if d.Name() == coredialect.NamePostgres {
		return "TIMESTAMPTZ NULL"
	}
	return "DATETIME NULL"
}

func archiveNextRetryAtDefinition(d coredialect.IDialect) string {
	if d.Name() == coredialect.NamePostgres {
		return "TIMESTAMPTZ NULL"
	}
	return "DATETIME NULL"
}

func splitQualifiedTableName(name string) (schemaName string, tableName string) {
	idx := strings.LastIndex(name, ".")
	if idx <= 0 || idx >= len(name)-1 {
		return "", name
	}
	return name[:idx], name[idx+1:]
}
