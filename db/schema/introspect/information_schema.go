package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"gochen-runtime/db/schema"
	"gochen/db"
	"gochen/errors"
)

// MySQLIntrospector 读取 MySQL information_schema 中的基础表、列和显式索引。
type MySQLIntrospector struct{}

// NewMySQL 创建 MySQL introspector。
func NewMySQL() *MySQLIntrospector { return &MySQLIntrospector{} }

// Inspect 读取 MySQL 当前 schema 快照。
func (i *MySQLIntrospector) Inspect(ctx context.Context, database db.IDatabase) (*schema.Schema, error) {
	result, err := inspectInformationSchema(ctx, database, informationSchemaQueries{
		tableSQL: `
	SELECT table_schema, table_name
	FROM information_schema.tables
	WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
	ORDER BY table_name`,
		columnSQL: `
SELECT column_name, column_type, is_nullable, column_default, column_key, extra
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY ordinal_position`,
		indexSQL: `
SELECT index_name, non_unique, column_name, seq_in_index, sub_part, collation
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY'
ORDER BY index_name, seq_in_index`,
		scanColumn: scanMySQLColumn,
		scanIndex:  scanMySQLIndex,
	})
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, "MySQL introspection skips foreign keys, check constraints, expression indexes, partial indexes, and complex constraints")
	return result, nil
}

// PostgresIntrospector 读取 PostgreSQL information_schema/pg_catalog 中的基础表、列和显式索引。
type PostgresIntrospector struct{}

// NewPostgres 创建 PostgreSQL introspector。
func NewPostgres() *PostgresIntrospector { return &PostgresIntrospector{} }

// Inspect 读取 PostgreSQL 当前 schema 快照。
func (i *PostgresIntrospector) Inspect(ctx context.Context, database db.IDatabase) (*schema.Schema, error) {
	result, err := inspectInformationSchema(ctx, database, informationSchemaQueries{
		tableSQL: `
	SELECT table_schema, table_name
	FROM information_schema.tables
	WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
	ORDER BY table_name`,
		columnSQL: `
	SELECT c.column_name,
	       c.data_type,
	       c.udt_name,
	       c.udt_schema,
	       c.domain_name,
	       c.domain_schema,
	       c.table_schema,
	       c.character_maximum_length,
	       c.numeric_precision,
	       c.numeric_scale,
       c.is_nullable,
       c.column_default,
       c.is_identity,
       EXISTS (
         SELECT 1
         FROM pg_class t
         JOIN pg_namespace n ON n.oid = t.relnamespace
         JOIN pg_index ix ON ix.indrelid = t.oid
         JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(ix.indkey)
         WHERE n.nspname = c.table_schema
           AND t.relkind = 'r'
           AND t.relname = c.table_name
           AND ix.indisprimary
           AND a.attname = c.column_name
       ) AS is_primary_key
FROM information_schema.columns c
WHERE c.table_schema = current_schema() AND c.table_name = ?
ORDER BY c.ordinal_position`,
		indexSQL: `
SELECT i.relname AS index_name,
       CASE WHEN ix.indisunique THEN 0 ELSE 1 END AS non_unique,
       a.attname AS column_name,
       k.ordinality AS seq_in_index,
       pg_get_indexdef(i.oid, k.ordinality::integer, false) AS key_def,
       ix.indnkeyatts,
       ix.indnatts,
       ix.indpred IS NOT NULL AS has_predicate,
       ix.indexprs IS NOT NULL AS has_expression
FROM pg_class t
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_index ix ON ix.indrelid = t.oid
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ordinality) ON true
LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
WHERE n.nspname = current_schema()
  AND t.relkind = 'r'
  AND t.relname = ?
  AND NOT ix.indisprimary
  AND k.ordinality <= ix.indnkeyatts
ORDER BY i.relname, k.ordinality`,
		scanColumn: scanPostgresColumn,
		scanIndex:  scanPostgresIndex,
	})
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, "Postgres introspection skips foreign keys, check constraints, expression indexes, partial indexes, and complex constraints")
	return result, nil
}

type informationSchemaQueries struct {
	tableSQL   string
	columnSQL  string
	indexSQL   string
	scanColumn func(db.IRows) (schema.Column, error)
	scanIndex  func(db.IRows) (informationSchemaIndexColumn, bool, error)
}

type informationSchemaIndexColumn struct {
	name    string
	unique  bool
	column  string
	ordinal int
}

type informationSchemaTableRef struct {
	schema string
	name   string
}

func inspectInformationSchema(ctx context.Context, database db.IDatabase, queries informationSchemaQueries) (*schema.Schema, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "database cannot be nil")
	}
	ctx = normalizeInspectContext(ctx)

	tableRows, err := database.Query(ctx, queries.tableSQL)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "list information_schema tables failed")
	}
	defer tableRows.Close()

	var tableRefs []informationSchemaTableRef
	for tableRows.Next() {
		var (
			schemaName string
			name       string
		)
		if err := tableRows.Scan(&schemaName, &name); err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan information_schema table failed")
		}
		tableRefs = append(tableRefs, informationSchemaTableRef{
			schema: strings.TrimSpace(schemaName),
			name:   strings.TrimSpace(name),
		})
	}
	if err := tableRows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate information_schema tables failed")
	}

	result := &schema.Schema{Tables: make([]schema.Table, 0, len(tableRefs))}
	for _, tableRef := range tableRefs {
		table, warnings, err := inspectInformationSchemaTable(ctx, database, queries, tableRef)
		if err != nil {
			return nil, err
		}
		result.Tables = append(result.Tables, table)
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result, nil
}

func inspectInformationSchemaTable(ctx context.Context, database db.IDatabase, queries informationSchemaQueries, tableRef informationSchemaTableRef) (schema.Table, []string, error) {
	table := schema.Table{Schema: tableRef.schema, Name: tableRef.name}

	columnRows, err := database.Query(ctx, queries.columnSQL, tableRef.name)
	if err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "query information_schema columns failed").
			WithContext("table", table.QualifiedName())
	}
	defer columnRows.Close()

	for columnRows.Next() {
		column, err := queries.scanColumn(columnRows)
		if err != nil {
			return table, nil, errors.Wrap(err, errors.Database, "scan information_schema column failed").
				WithContext("table", table.QualifiedName())
		}
		table.Columns = append(table.Columns, column)
	}
	if err := columnRows.Err(); err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "iterate information_schema columns failed").
			WithContext("table", table.QualifiedName())
	}

	indexes, warnings, err := inspectInformationSchemaIndexes(ctx, database, queries.indexSQL, queries.scanIndex, table)
	if err != nil {
		return table, nil, err
	}
	table.Indexes = indexes
	return table, warnings, nil
}

func scanMySQLColumn(rows db.IRows) (schema.Column, error) {
	var (
		name     string
		typ      string
		nullable string
		def      sql.NullString
		key      string
		extra    string
	)
	if err := rows.Scan(&name, &typ, &nullable, &def, &key, &extra); err != nil {
		return schema.Column{}, err
	}
	normalizedType := schema.NormalizeColumnType(typ)
	defaultValue := def.String
	if def.Valid && defaultValue == "" {
		defaultValue = "''"
	}
	return schema.Column{
		Name:          name,
		Type:          normalizedType,
		Nullable:      strings.EqualFold(nullable, "YES"),
		DefaultValue:  schema.NormalizeDefaultValue(normalizedType, defaultValue, "mysql"),
		PrimaryKey:    key == "PRI",
		AutoIncrement: strings.Contains(strings.ToLower(extra), "auto_increment"),
	}, nil
}

func scanPostgresColumn(rows db.IRows) (schema.Column, error) {
	var (
		name         string
		dataType     string
		udtName      sql.NullString
		udtSchema    sql.NullString
		domain       sql.NullString
		domainSchema sql.NullString
		tableSchema  string
		maxLength    sql.NullInt64
		precision    sql.NullInt64
		scale        sql.NullInt64
		nullable     string
		def          sql.NullString
		identity     string
		primary      bool
	)
	if err := rows.Scan(
		&name,
		&dataType,
		&udtName,
		&udtSchema,
		&domain,
		&domainSchema,
		&tableSchema,
		&maxLength,
		&precision,
		&scale,
		&nullable,
		&def,
		&identity,
		&primary,
	); err != nil {
		return schema.Column{}, err
	}
	typ := schema.NormalizeColumnType(formatPostgresType(
		dataType,
		udtName.String,
		udtSchema.String,
		domain.String,
		domainSchema.String,
		tableSchema,
		maxLength,
		precision,
		scale,
	))
	autoIncrement := strings.HasPrefix(strings.ToLower(strings.TrimSpace(def.String)), "nextval(") || strings.EqualFold(identity, "YES")
	defaultValue := schema.NormalizeDefaultValue(typ, def.String, "postgres")
	if autoIncrement {
		defaultValue = ""
	}
	return schema.Column{
		Name:          name,
		Type:          typ,
		Nullable:      strings.EqualFold(nullable, "YES"),
		DefaultValue:  defaultValue,
		PrimaryKey:    primary,
		AutoIncrement: autoIncrement,
	}, nil
}

func formatPostgresType(
	dataType string,
	udtName string,
	udtSchema string,
	domainName string,
	domainSchema string,
	tableSchema string,
	maxLength sql.NullInt64,
	precision sql.NullInt64,
	scale sql.NullInt64,
) string {
	typ := strings.ToUpper(strings.TrimSpace(dataType))
	if domain := strings.TrimSpace(domainName); domain != "" {
		return qualifyPostgresTypeName(domainSchema, tableSchema, domain)
	}
	switch typ {
	case "ARRAY":
		return formatPostgresArrayType(udtName, udtSchema, tableSchema)
	case "CHARACTER VARYING":
		if maxLength.Valid && maxLength.Int64 > 0 {
			return fmt.Sprintf("VARCHAR(%d)", maxLength.Int64)
		}
		return "VARCHAR"
	case "CHARACTER":
		if maxLength.Valid && maxLength.Int64 > 0 {
			return fmt.Sprintf("CHAR(%d)", maxLength.Int64)
		}
		return "CHAR"
	case "NUMERIC", "DECIMAL":
		if precision.Valid && precision.Int64 > 0 {
			if scale.Valid && scale.Int64 >= 0 {
				return fmt.Sprintf("%s(%d,%d)", typ, precision.Int64, scale.Int64)
			}
			return fmt.Sprintf("%s(%d)", typ, precision.Int64)
		}
		return typ
	case "TIMESTAMP WITHOUT TIME ZONE":
		return "TIMESTAMP"
	case "TIMESTAMP WITH TIME ZONE":
		return "TIMESTAMPTZ"
	case "TIME WITHOUT TIME ZONE":
		return "TIME"
	case "TIME WITH TIME ZONE":
		return "TIMETZ"
	case "BYTEA":
		return "BYTEA"
	case "USER-DEFINED":
		if udt := strings.TrimSpace(udtName); udt != "" {
			return qualifyPostgresTypeName(udtSchema, tableSchema, udt)
		}
		return typ
	default:
		return typ
	}
}

func qualifyPostgresTypeName(typeSchema string, currentSchema string, name string) string {
	typeSchema = strings.TrimSpace(typeSchema)
	currentSchema = strings.TrimSpace(currentSchema)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if typeSchema == "" || strings.EqualFold(typeSchema, currentSchema) {
		return name
	}
	return typeSchema + "." + name
}

func formatPostgresArrayType(udtName string, udtSchema string, currentSchema string) string {
	udtName = strings.TrimSpace(udtName)
	udtName = strings.TrimPrefix(udtName, "_")
	if udtName == "" {
		return "ARRAY"
	}
	if builtin, ok := postgresBuiltinUdtNameToType(udtName); ok {
		return builtin + "[]"
	}
	if strings.EqualFold(strings.TrimSpace(udtSchema), "pg_catalog") {
		return strings.ToUpper(udtName) + "[]"
	}
	return qualifyPostgresTypeName(udtSchema, currentSchema, udtName) + "[]"
}

func postgresBuiltinUdtNameToType(udtName string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(udtName)) {
	case "bool":
		return "BOOLEAN", true
	case "int2":
		return "SMALLINT", true
	case "int4":
		return "INTEGER", true
	case "int8":
		return "BIGINT", true
	case "float4":
		return "REAL", true
	case "float8":
		return "DOUBLE PRECISION", true
	case "varchar":
		return "VARCHAR", true
	case "bpchar":
		return "CHAR", true
	case "timestamptz":
		return "TIMESTAMPTZ", true
	case "timetz":
		return "TIMETZ", true
	default:
		return "", false
	}
}

func inspectInformationSchemaIndexes(
	ctx context.Context,
	database db.IDatabase,
	query string,
	scanIndex func(db.IRows) (informationSchemaIndexColumn, bool, error),
	table schema.Table,
) ([]schema.Index, []string, error) {
	rows, err := database.Query(ctx, query, table.Name)
	if err != nil {
		return nil, nil, errors.Wrap(err, errors.Database, "query information_schema indexes failed").
			WithContext("table", table.QualifiedName())
	}
	defer rows.Close()

	var columns []informationSchemaIndexColumn
	skipped := make(map[string]bool)
	for rows.Next() {
		indexColumn, simple, err := scanIndex(rows)
		if err != nil {
			return nil, nil, errors.Wrap(err, errors.Database, "scan information_schema index failed").
				WithContext("table", table.QualifiedName())
		}
		if !simple {
			skipped[indexColumn.name] = true
			continue
		}
		columns = append(columns, indexColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, errors.Wrap(err, errors.Database, "iterate information_schema indexes failed").
			WithContext("table", table.QualifiedName())
	}

	sort.Slice(columns, func(i, j int) bool {
		if columns[i].name == columns[j].name {
			return columns[i].ordinal < columns[j].ordinal
		}
		return columns[i].name < columns[j].name
	})

	var indexes []schema.Index
	for _, column := range columns {
		if skipped[column.name] {
			continue
		}
		if len(indexes) == 0 || indexes[len(indexes)-1].Name != column.name {
			indexes = append(indexes, schema.Index{Name: column.name, Unique: column.unique})
		}
		indexes[len(indexes)-1].Columns = append(indexes[len(indexes)-1].Columns, column.column)
	}
	warnings := make([]string, 0, len(skipped))
	for name := range skipped {
		indexes = append(indexes, schema.Index{Name: name, Unsupported: true})
		warnings = append(warnings, fmt.Sprintf("introspection skipped complex index %s.%s", table.QualifiedName(), name))
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i].Name < indexes[j].Name })
	sort.Strings(warnings)
	return indexes, warnings, nil
}

func scanMySQLIndex(rows db.IRows) (informationSchemaIndexColumn, bool, error) {
	var (
		name      string
		nonUnique int
		column    sql.NullString
		ordinal   int
		subPart   sql.NullInt64
		collation sql.NullString
	)
	if err := rows.Scan(&name, &nonUnique, &column, &ordinal, &subPart, &collation); err != nil {
		return informationSchemaIndexColumn{}, false, err
	}
	simple := column.Valid && strings.TrimSpace(column.String) != "" && !subPart.Valid && !strings.EqualFold(collation.String, "D")
	return informationSchemaIndexColumn{
		name:    name,
		unique:  nonUnique == 0,
		column:  strings.TrimSpace(column.String),
		ordinal: ordinal,
	}, simple, nil
}

func scanPostgresIndex(rows db.IRows) (informationSchemaIndexColumn, bool, error) {
	var (
		name          string
		nonUnique     int
		column        sql.NullString
		ordinal       int
		keyDef        string
		keyAttrCount  int
		attrCount     int
		hasPredicate  bool
		hasExpression bool
	)
	if err := rows.Scan(&name, &nonUnique, &column, &ordinal, &keyDef, &keyAttrCount, &attrCount, &hasPredicate, &hasExpression); err != nil {
		return informationSchemaIndexColumn{}, false, err
	}
	columnName := strings.TrimSpace(column.String)
	keyDef = normalizePostgresIndexKeyDef(keyDef)
	simple := !hasPredicate && !hasExpression && column.Valid && columnName != "" && keyAttrCount == attrCount && keyDef == columnName
	return informationSchemaIndexColumn{
		name:    name,
		unique:  nonUnique == 0,
		column:  columnName,
		ordinal: ordinal,
	}, simple, nil
}

func normalizePostgresIndexKeyDef(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = strings.ReplaceAll(value[1:len(value)-1], `""`, `"`)
	}
	return value
}
