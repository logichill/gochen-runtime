package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/schema"
	"gochen/contextx"
	"gochen/db"
	"gochen/errors"
)

// SQLiteIntrospector 读取 SQLite 数据库结构。
type SQLiteIntrospector struct{}

// NewSQLite 创建 SQLite introspector。
func NewSQLite() *SQLiteIntrospector { return &SQLiteIntrospector{} }

// Inspect 读取 SQLite 的表、列与显式索引结构。
func (i *SQLiteIntrospector) Inspect(ctx context.Context, database db.IDatabase) (*schema.Schema, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "database cannot be nil")
	}
	ctx = normalizeInspectContext(ctx)

	rows, err := database.Query(ctx, "SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "list sqlite tables failed")
	}

	type tableDef struct {
		name      string
		createSQL string
	}
	var tables []tableDef
	for rows.Next() {
		var (
			name    string
			createV sql.NullString
		)
		if err := rows.Scan(&name, &createV); err != nil {
			_ = rows.Close()
			return nil, errors.Wrap(err, errors.Database, "scan sqlite table failed")
		}
		tables = append(tables, tableDef{name: name, createSQL: createV.String})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, errors.Wrap(err, errors.Database, "iterate sqlite tables failed")
	}
	if err := rows.Close(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "close sqlite tables rows failed")
	}

	result := &schema.Schema{}
	for _, def := range tables {
		table, warnings, err := i.inspectTable(ctx, database, def.name, def.createSQL)
		if err != nil {
			return nil, err
		}
		result.Tables = append(result.Tables, table)
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result, nil
}

func (i *SQLiteIntrospector) inspectTable(ctx context.Context, database db.IDatabase, tableName string, createSQL string) (schema.Table, []string, error) {
	table := schema.Table{Name: tableName}
	var warnings []string

	quotedTable := dialect.New("sqlite").QuoteIdentifier(tableName)
	columnRows, err := database.Query(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quotedTable))
	if err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "query sqlite table_info failed").
			WithContext("table", tableName)
	}

	for columnRows.Next() {
		var (
			cid       int
			name      string
			typ       string
			notNull   int
			defaultV  sql.NullString
			pkOrdinal int
		)
		if err := columnRows.Scan(&cid, &name, &typ, &notNull, &defaultV, &pkOrdinal); err != nil {
			_ = columnRows.Close()
			return table, nil, errors.Wrap(err, errors.Database, "scan sqlite column failed").
				WithContext("table", tableName)
		}
		nullable := notNull == 0
		if pkOrdinal > 0 {
			nullable = false
		}
		table.Columns = append(table.Columns, schema.Column{
			Name:          name,
			Type:          schema.NormalizeColumnType(typ),
			Nullable:      nullable,
			DefaultValue:  strings.TrimSpace(defaultV.String),
			PrimaryKey:    pkOrdinal > 0,
			AutoIncrement: pkOrdinal > 0 && detectSQLiteAutoIncrement(createSQL, name),
		})
	}
	if err := columnRows.Err(); err != nil {
		_ = columnRows.Close()
		return table, nil, errors.Wrap(err, errors.Database, "iterate sqlite columns failed").
			WithContext("table", tableName)
	}
	if err := columnRows.Close(); err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "close sqlite columns rows failed").
			WithContext("table", tableName)
	}

	indexRows, err := database.Query(ctx, fmt.Sprintf("PRAGMA index_list(%s)", quotedTable))
	if err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "query sqlite index_list failed").
			WithContext("table", tableName)
	}

	type sqliteIndexInfo struct {
		name    string
		unique  bool
		partial bool
	}
	var indexes []sqliteIndexInfo
	for indexRows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := indexRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = indexRows.Close()
			return table, nil, errors.Wrap(err, errors.Database, "scan sqlite index failed").
				WithContext("table", tableName)
		}
		_ = seq
		// SQLite 的 UNIQUE constraint 会生成 origin=u 的 autoindex；保留它用于避免重复生成等价唯一索引。
		if origin != "c" && origin != "u" {
			continue
		}
		indexes = append(indexes, sqliteIndexInfo{name: name, unique: unique != 0, partial: partial != 0})
	}
	if err := indexRows.Err(); err != nil {
		_ = indexRows.Close()
		return table, nil, errors.Wrap(err, errors.Database, "iterate sqlite indexes failed").
			WithContext("table", tableName)
	}
	if err := indexRows.Close(); err != nil {
		return table, nil, errors.Wrap(err, errors.Database, "close sqlite index rows failed").
			WithContext("table", tableName)
	}

	for _, index := range indexes {
		if index.partial {
			warnings = append(warnings, fmt.Sprintf("SQLite introspection skipped partial index %s.%s", tableName, index.name))
			table.Indexes = append(table.Indexes, schema.Index{Name: index.name, Unsupported: true})
			continue
		}
		columns, skippedComplex, err := inspectSQLiteIndexColumns(ctx, database, index.name)
		if err != nil {
			return table, nil, err
		}
		if skippedComplex {
			warnings = append(warnings, fmt.Sprintf("SQLite introspection skipped expression index %s.%s", tableName, index.name))
			table.Indexes = append(table.Indexes, schema.Index{Name: index.name, Unsupported: true})
			continue
		}
		if len(columns) == 0 {
			continue
		}
		table.Indexes = append(table.Indexes, schema.Index{
			Name:    index.name,
			Columns: columns,
			Unique:  index.unique,
		})
	}

	sort.Slice(table.Indexes, func(i, j int) bool { return table.Indexes[i].Name < table.Indexes[j].Name })
	sort.Strings(warnings)
	return table, warnings, nil
}

func inspectSQLiteIndexColumns(ctx context.Context, database db.IDatabase, indexName string) ([]string, bool, error) {
	rows, err := database.Query(ctx, fmt.Sprintf("PRAGMA index_xinfo(%s)", dialect.New("sqlite").QuoteIdentifier(indexName)))
	if err != nil {
		return nil, false, errors.Wrap(err, errors.Database, "query sqlite index_xinfo failed").
			WithContext("index", indexName)
	}
	defer rows.Close()

	type indexColumn struct {
		seqno int
		name  string
	}
	var columns []indexColumn
	skippedComplex := false
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
			desc  int
			coll  sql.NullString
			key   int
		)
		if err := rows.Scan(&seqno, &cid, &name, &desc, &coll, &key); err != nil {
			return nil, false, errors.Wrap(err, errors.Database, "scan sqlite index column failed").
				WithContext("index", indexName)
		}
		_, _ = desc, coll
		if key == 0 {
			continue
		}
		if cid < 0 || !name.Valid || strings.TrimSpace(name.String) == "" {
			skippedComplex = true
			continue
		}
		columns = append(columns, indexColumn{seqno: seqno, name: name.String})
	}
	if err := rows.Err(); err != nil {
		return nil, false, errors.Wrap(err, errors.Database, "iterate sqlite index columns failed").
			WithContext("index", indexName)
	}

	if skippedComplex {
		return nil, true, nil
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].seqno < columns[j].seqno })
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}
	return names, false, nil
}

func detectSQLiteAutoIncrement(createSQL string, columnName string) bool {
	upper := strings.ToUpper(createSQL)
	if !strings.Contains(upper, "AUTOINCREMENT") {
		return false
	}
	for _, definition := range splitSQLiteCreateDefinitions(createSQL) {
		name, remainder, ok := parseSQLiteColumnDefinition(definition)
		if !ok || !strings.EqualFold(name, columnName) {
			continue
		}
		return containsSQLiteKeyword(remainder, "AUTOINCREMENT")
	}
	return false
}

func normalizeInspectContext(ctx context.Context) context.Context {
	if ctx == nil {
		return contextx.Background()
	}
	return ctx
}

func splitSQLiteCreateDefinitions(createSQL string) []string {
	start := strings.IndexByte(createSQL, '(')
	if start < 0 {
		return nil
	}
	var (
		definitions []string
		sb          strings.Builder
		depth       = 1
		inSingle    bool
		inDouble    bool
		inBacktick  bool
		inBracket   bool
	)
	for i := start + 1; i < len(createSQL); i++ {
		ch := createSQL[i]
		next := byte(0)
		if i+1 < len(createSQL) {
			next = createSQL[i+1]
		}
		switch {
		case inSingle:
			sb.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					sb.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		case inDouble:
			sb.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					sb.WriteByte(next)
					i++
					continue
				}
				inDouble = false
			}
			continue
		case inBacktick:
			sb.WriteByte(ch)
			if ch == '`' {
				inBacktick = false
			}
			continue
		case inBracket:
			sb.WriteByte(ch)
			if ch == ']' {
				inBracket = false
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '[':
			inBracket = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				appendSQLiteDefinition(&definitions, sb.String())
				return definitions
			}
		case ',':
			if depth == 1 {
				appendSQLiteDefinition(&definitions, sb.String())
				sb.Reset()
				continue
			}
		}
		sb.WriteByte(ch)
	}
	appendSQLiteDefinition(&definitions, sb.String())
	return definitions
}

func appendSQLiteDefinition(definitions *[]string, definition string) {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return
	}
	*definitions = append(*definitions, definition)
}

func parseSQLiteColumnDefinition(definition string) (string, string, bool) {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return "", "", false
	}
	first, next, ok := readSQLiteIdentifierToken(definition, 0)
	if !ok {
		return "", "", false
	}
	if isSQLiteTableConstraint(first) {
		return "", "", false
	}
	return first, definition[next:], true
}

func readSQLiteIdentifierToken(text string, start int) (string, int, bool) {
	i := skipSQLiteSpace(text, start)
	if i >= len(text) {
		return "", 0, false
	}
	switch text[i] {
	case '"':
		end := i + 1
		for end < len(text) {
			if text[end] == '"' {
				if end+1 < len(text) && text[end+1] == '"' {
					end += 2
					continue
				}
				return strings.ReplaceAll(text[i+1:end], `""`, `"`), end + 1, true
			}
			end++
		}
		return strings.ReplaceAll(text[i+1:], `""`, `"`), len(text), true
	case '`':
		end := strings.IndexByte(text[i+1:], '`')
		if end < 0 {
			return text[i+1:], len(text), true
		}
		end += i + 1
		return text[i+1 : end], end + 1, true
	case '[':
		end := strings.IndexByte(text[i+1:], ']')
		if end < 0 {
			return text[i+1:], len(text), true
		}
		end += i + 1
		return text[i+1 : end], end + 1, true
	default:
		end := i
		for end < len(text) && !isSQLiteDefinitionSpace(text[end]) && text[end] != '(' && text[end] != ',' {
			end++
		}
		return text[i:end], end, end > i
	}
}

func isSQLiteTableConstraint(token string) bool {
	switch strings.ToUpper(strings.TrimSpace(token)) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
		return true
	default:
		return false
	}
}

func containsSQLiteKeyword(text string, keyword string) bool {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	for i := 0; i < len(text); {
		ch := text[i]
		switch ch {
		case '\'', '"', '`', '[':
			i = skipSQLiteQuoted(text, i)
			continue
		}
		if isSQLiteIdentifierStart(ch) {
			start := i
			i++
			for i < len(text) && isSQLiteIdentifierPart(text[i]) {
				i++
			}
			if strings.EqualFold(text[start:i], keyword) {
				return true
			}
			continue
		}
		i++
	}
	return false
}

func skipSQLiteQuoted(text string, start int) int {
	if start >= len(text) {
		return start
	}
	switch text[start] {
	case '\'':
		for i := start + 1; i < len(text); i++ {
			if text[i] == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
	case '"':
		for i := start + 1; i < len(text); i++ {
			if text[i] == '"' {
				if i+1 < len(text) && text[i+1] == '"' {
					i++
					continue
				}
				return i + 1
			}
		}
	case '`':
		if end := strings.IndexByte(text[start+1:], '`'); end >= 0 {
			return start + end + 2
		}
	case '[':
		if end := strings.IndexByte(text[start+1:], ']'); end >= 0 {
			return start + end + 2
		}
	}
	return len(text)
}

func skipSQLiteSpace(text string, start int) int {
	for start < len(text) && isSQLiteDefinitionSpace(text[start]) {
		start++
	}
	return start
}

func isSQLiteDefinitionSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func isSQLiteIdentifierStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isSQLiteIdentifierPart(ch byte) bool {
	return isSQLiteIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}
