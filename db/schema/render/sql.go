package render

import (
	"fmt"
	coredialect "gochen/db/dialect"
	"strings"

	"gochen-runtime/db/schema"
	"gochen-runtime/db/schema/diff"
	"gochen-runtime/db/sql/safeident"

	"gochen-runtime/db/sqlscan"
	"gochen/errors"
)

// RenderSQL 把 schema 变更渲染为指定方言的 SQL。
func RenderSQL(changes diff.Changes, d coredialect.IDialect) ([]string, error) {
	var statements []string
	for _, change := range changes {
		switch change.Kind {
		case diff.KindAddTable:
			tableName, err := addTableName(change)
			if err != nil {
				return nil, err
			}
			statement, err := renderCreateTable(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
			if change.Target != nil {
				for _, index := range change.Target.Indexes {
					idx := index
					stmt, err := renderCreateIndex(tableName, &idx, d)
					if err != nil {
						return nil, err
					}
					statements = append(statements, stmt)
				}
			}
		case diff.KindAddColumn:
			statement, err := renderAddColumn(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
		case diff.KindAddIndex:
			statement, err := renderAddIndex(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
		default:
			return nil, errors.NewCode(errors.Unsupported, fmt.Sprintf("unsupported schema change kind: %s", change.Kind)).
				WithContext("kind", change.Kind)
		}
	}
	return statements, nil
}

func addTableName(change diff.Change) (string, error) {
	if change.Target == nil {
		return "", errors.NewCode(errors.InvalidInput, "add table change missing target table")
	}
	tableName := strings.TrimSpace(change.Target.QualifiedName())
	if strings.TrimSpace(change.Table) != "" && strings.TrimSpace(change.Table) != tableName {
		return "", errors.NewCode(errors.InvalidInput, fmt.Sprintf("add table change table mismatch: table=%q target=%q", change.Table, tableName)).
			WithContext("table", change.Table).
			WithContext("target", tableName)
	}
	return tableName, nil
}

// RenderDownSQL 把新增类 schema 变更反向渲染为回滚 SQL。
func RenderDownSQL(changes diff.Changes, d coredialect.IDialect) ([]string, error) {
	var statements []string
	for i := len(changes) - 1; i >= 0; i-- {
		change := changes[i]
		switch change.Kind {
		case diff.KindAddTable:
			statement, err := renderDropTable(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
		case diff.KindAddColumn:
			statement, err := renderDropColumn(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
		case diff.KindAddIndex:
			statement, err := renderDropIndex(change, d)
			if err != nil {
				return nil, err
			}
			statements = append(statements, statement)
		default:
			return nil, errors.NewCode(errors.Unsupported, fmt.Sprintf("unsupported schema change kind: %s", change.Kind)).
				WithContext("kind", change.Kind)
		}
	}
	return statements, nil
}

func renderCreateTable(change diff.Change, d coredialect.IDialect) (string, error) {
	if change.Target == nil {
		return "", errors.NewCode(errors.InvalidInput, "add table change missing target table")
	}
	tableName := change.Target.QualifiedName()
	if err := validateQualifiedIdentifier(tableName, "table"); err != nil {
		return "", err
	}
	if len(change.Target.Columns) == 0 {
		return "", errors.NewCode(errors.InvalidInput, fmt.Sprintf("table %s has no columns", tableName)).
			WithContext("table", tableName)
	}
	if err := validateCreateTablePrimaryKeyLayout(change.Target.Columns, d); err != nil {
		return "", err
	}

	defs := make([]string, 0, len(change.Target.Columns)+1)
	var primary []string
	inlinePrimaryKeyColumn := inlinePrimaryKeyColumnName(change.Target.Columns, d)
	for _, column := range change.Target.Columns {
		def, inlinePK, err := renderColumn(columnSpec{
			Column:               column,
			TableCreating:        true,
			InlinePrimaryKeyName: inlinePrimaryKeyColumn,
		}, d)
		if err != nil {
			return "", err
		}
		defs = append(defs, def)
		if column.PrimaryKey && !inlinePK {
			if err := validateSimpleIdentifier(column.Name, "primary key column"); err != nil {
				return "", err
			}
			primary = append(primary, d.QuoteIdentifier(column.Name))
		}
	}
	if len(primary) > 0 {
		defs = append(defs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(primary, ", ")))
	}

	return fmt.Sprintf(
		"CREATE TABLE %s (\n  %s\n)",
		d.QuoteIdentifier(tableName),
		strings.Join(defs, ",\n  "),
	), nil
}

func renderDropTable(change diff.Change, d coredialect.IDialect) (string, error) {
	if err := validateQualifiedIdentifier(change.Table, "table"); err != nil {
		return "", err
	}
	return fmt.Sprintf("DROP TABLE %s", d.QuoteIdentifier(change.Table)), nil
}

func renderAddColumn(change diff.Change, d coredialect.IDialect) (string, error) {
	if err := validateQualifiedIdentifier(change.Table, "table"); err != nil {
		return "", err
	}
	if change.Column == nil {
		return "", errors.NewCode(errors.InvalidInput, "add column change missing column")
	}
	def, _, err := renderColumn(columnSpec{Column: *change.Column}, d)
	if err != nil {
		return "", err
	}
	if d.SchemaFeatures().AddNotNullColumnRequiresDefault && !change.Column.Nullable && strings.TrimSpace(change.Column.DefaultValue) == "" {
		return "", errors.NewCode(errors.Unsupported, fmt.Sprintf("%s add column %s cannot be NOT NULL without default", d.Name(), change.Column.Name)).
			WithContext("column", change.Column.Name).
			WithContext("dialect", d.Name())
	}
	return fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN %s",
		d.QuoteIdentifier(change.Table),
		def,
	), nil
}

func renderDropColumn(change diff.Change, d coredialect.IDialect) (string, error) {
	if err := validateQualifiedIdentifier(change.Table, "table"); err != nil {
		return "", err
	}
	if change.Column == nil {
		return "", errors.NewCode(errors.InvalidInput, "drop column change missing column")
	}
	if err := validateSimpleIdentifier(change.Column.Name, "column"); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"ALTER TABLE %s DROP COLUMN %s",
		d.QuoteIdentifier(change.Table),
		d.QuoteIdentifier(change.Column.Name),
	), nil
}

func renderAddIndex(change diff.Change, d coredialect.IDialect) (string, error) {
	return renderCreateIndex(change.Table, change.Index, d)
}

func renderCreateIndex(table string, index *schema.Index, d coredialect.IDialect) (string, error) {
	if index == nil {
		return "", errors.NewCode(errors.InvalidInput, "add index change missing index")
	}
	if err := validateQualifiedIdentifier(table, "table"); err != nil {
		return "", err
	}
	if err := validateSimpleIdentifier(index.Name, "index"); err != nil {
		return "", err
	}
	if len(index.Columns) == 0 {
		return "", errors.NewCode(errors.InvalidInput, fmt.Sprintf("index %s has no columns", index.Name)).
			WithContext("index", index.Name)
	}

	quotedColumns := make([]string, 0, len(index.Columns))
	for _, column := range index.Columns {
		column = strings.TrimSpace(column)
		if err := validateSimpleIdentifier(column, "index column"); err != nil {
			return "", err
		}
		quotedColumns = append(quotedColumns, d.QuoteIdentifier(column))
	}

	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	indexName, tableName := d.CreateIndexTargets(table, index.Name)
	return fmt.Sprintf(
		"CREATE %sINDEX %s ON %s (%s)",
		unique,
		d.QuoteIdentifier(indexName),
		d.QuoteIdentifier(tableName),
		strings.Join(quotedColumns, ", "),
	), nil
}

func renderDropIndex(change diff.Change, d coredialect.IDialect) (string, error) {
	if err := validateQualifiedIdentifier(change.Table, "table"); err != nil {
		return "", err
	}
	if change.Index == nil {
		return "", errors.NewCode(errors.InvalidInput, "drop index change missing index")
	}
	if err := validateSimpleIdentifier(change.Index.Name, "index"); err != nil {
		return "", err
	}
	return d.DropIndexSQL(change.Table, change.Index.Name), nil
}

type columnSpec struct {
	Column               schema.Column
	TableCreating        bool
	InlinePrimaryKeyName string
}

func renderColumn(spec columnSpec, d coredialect.IDialect) (definition string, inlinePrimaryKey bool, err error) {
	column := spec.Column
	if err := validateSimpleIdentifier(column.Name, "column"); err != nil {
		return "", false, err
	}
	typ := strings.TrimSpace(column.Type)
	if typ == "" {
		return "", false, errors.NewCode(errors.InvalidInput, fmt.Sprintf("column %s type cannot be empty", column.Name)).
			WithContext("column", column.Name)
	}
	if err := validateRawSQLFragment(typ, "column type"); err != nil {
		return "", false, err
	}
	if defaultValue := strings.TrimSpace(column.DefaultValue); defaultValue != "" {
		if err := validateRawSQLFragment(defaultValue, "column default"); err != nil {
			return "", false, err
		}
	}

	parts := []string{d.QuoteIdentifier(column.Name), renderColumnType(column, d)}
	autoIncrement := d.SchemaFeatures().AutoIncrement
	inlinePK := false
	if column.PrimaryKey && column.AutoIncrement && spec.TableCreating && spec.InlinePrimaryKeyName == column.Name {
		if autoIncrement == coredialect.AutoIncrementSQLite {
			inlinePK = true
			parts = []string{d.QuoteIdentifier(column.Name), "INTEGER PRIMARY KEY AUTOINCREMENT"}
		}
	} else if column.PrimaryKey && spec.TableCreating {
		inlinePK = false
	} else if column.PrimaryKey {
		return "", false, errors.NewCode(errors.Unsupported, "adding primary key column is not supported").
			WithContext("column", column.Name)
	} else if column.AutoIncrement && (autoIncrement == coredialect.AutoIncrementSQLite || autoIncrement == coredialect.AutoIncrementMySQL) {
		return "", false, errors.NewCode(errors.InvalidInput, fmt.Sprintf("dialect %s auto increment column %s must be primary key", d.Name(), column.Name)).
			WithContext("column", column.Name).
			WithContext("dialect", d.Name())
	}

	if !inlinePK {
		if !column.Nullable || column.AutoIncrement {
			parts = append(parts, "NOT NULL")
		}
		if column.AutoIncrement && autoIncrement == coredialect.AutoIncrementMySQL {
			parts = append(parts, "AUTO_INCREMENT")
		}
		if strings.TrimSpace(column.DefaultValue) != "" && !column.AutoIncrement {
			parts = append(parts, "DEFAULT", strings.TrimSpace(column.DefaultValue))
		}
	}

	return strings.Join(parts, " "), inlinePK, nil
}

func validateCreateTablePrimaryKeyLayout(columns []schema.Column, d coredialect.IDialect) error {
	if d.SchemaFeatures().AutoIncrement != coredialect.AutoIncrementSQLite {
		return nil
	}
	primaryCount := 0
	autoIncrementPrimaryCount := 0
	for _, column := range columns {
		if !column.PrimaryKey {
			continue
		}
		primaryCount++
		if column.AutoIncrement {
			autoIncrementPrimaryCount++
		}
	}
	if primaryCount > 1 && autoIncrementPrimaryCount > 0 {
		return errors.NewCode(errors.InvalidInput, "auto increment column requires a single-column primary key").
			WithContext("dialect", d.Name())
	}
	if autoIncrementPrimaryCount > 1 {
		return errors.NewCode(errors.InvalidInput, "dialect supports at most one auto increment primary key column").
			WithContext("dialect", d.Name())
	}
	return nil
}

func inlinePrimaryKeyColumnName(columns []schema.Column, d coredialect.IDialect) string {
	if d.SchemaFeatures().AutoIncrement != coredialect.AutoIncrementSQLite {
		return ""
	}
	for _, column := range columns {
		if column.PrimaryKey && column.AutoIncrement {
			return column.Name
		}
	}
	return ""
}

func validateQualifiedIdentifier(name string, source string) error {
	if err := safeident.ValidateQualifiedIdentifier(name, source, 2); err != nil {
		return errors.NewCodeWithCause(errors.InvalidInput, fmt.Sprintf("unsafe schema %s identifier: %q", source, name), err).
			WithContext("source", source).
			WithContext("identifier", name)
	}
	return nil
}

func validateSimpleIdentifier(name string, source string) error {
	if err := safeident.ValidateQualifiedIdentifier(name, source, 1); err != nil {
		return errors.NewCodeWithCause(errors.InvalidInput, fmt.Sprintf("unsafe schema %s identifier: %q", source, name), err).
			WithContext("source", source).
			WithContext("identifier", name)
	}
	return nil
}

func renderColumnType(column schema.Column, d coredialect.IDialect) string {
	typ := strings.TrimSpace(column.Type)
	if !column.AutoIncrement {
		return typ
	}
	if d.SchemaFeatures().AutoIncrement == coredialect.AutoIncrementPostgresSerial {
		switch strings.ToUpper(typ) {
		case "SMALLINT", "INT2":
			return "SMALLSERIAL"
		case "BIGINT", "INT8":
			return "BIGSERIAL"
		case "INTEGER", "INTEGER4", "INT", "INT4":
			return "SERIAL"
		}
	}
	return typ
}

func validateRawSQLFragment(fragment string, source string) error {
	for i := 0; i < len(fragment); {
		ch := fragment[i]
		switch {
		case (ch == 'e' || ch == 'E') && i+1 < len(fragment) && fragment[i+1] == '\'':
			i = sqlscan.ScanBackslashSingleQuoted(fragment, i+1)
			continue
		case ch == '\'':
			i = sqlscan.ScanSingleQuoted(fragment, i)
			continue
		case ch == '"':
			i = sqlscan.ScanQuoted(fragment, i, '"')
			continue
		case ch == '$':
			if next, ok := sqlscan.ScanDollarQuoted(fragment, i); ok {
				i = next
				continue
			}
		}

		// 在字面量外部，检查危险字符与注释符号
		if strings.HasPrefix(fragment[i:], ";") ||
			strings.HasPrefix(fragment[i:], "--") ||
			strings.HasPrefix(fragment[i:], "/*") ||
			strings.HasPrefix(fragment[i:], "*/") {
			return unsafeFragmentError(source, fragment)
		}

		// 检查 dangerous token
		if sqlscan.IsIdentStart(ch) {
			start := i
			i++
			for i < len(fragment) && sqlscan.IsIdentPart(fragment[i]) {
				i++
			}
			if isDangerousSQLToken(fragment[start:i]) && !isAllowedSQLFragmentToken(fragment, source, start, i) {
				return unsafeFragmentError(source, fragment)
			}
			continue
		}
		i++
	}
	return nil
}

func unsafeFragmentError(source, fragment string) error {
	return errors.NewCode(errors.InvalidInput, fmt.Sprintf("unsafe schema %s fragment: %q", source, fragment)).
		WithContext("source", source).
		WithContext("fragment", fragment)
}

func isDangerousSQLToken(token string) bool {
	switch strings.ToUpper(token) {
	case "ALTER", "ATTACH", "CALL", "COPY", "CREATE", "DELETE", "DETACH", "DO", "DROP",
		"EXEC", "EXECUTE", "FROM", "GRANT", "INSERT", "INTO", "LOAD", "MERGE",
		"PRAGMA", "REINDEX", "REPLACE", "REVOKE", "SELECT", "SET", "TRUNCATE",
		"UNION", "UPDATE", "VACUUM", "VALUES", "WHERE", "WITH":
		return true
	default:
		return false
	}
}

// isAllowedSQLFragmentToken 是 schema renderer 的保守白名单。
// 新增允许的 SQL 关键字上下文必须先确认不会放宽 DDL 片段注入边界。
func isAllowedSQLFragmentToken(fragment string, source string, start, end int) bool {
	token := strings.ToUpper(fragment[start:end])
	switch token {
	case "WITH":
		before := strings.TrimSpace(fragment[:start])
		after := strings.TrimSpace(fragment[end:])
		beforeUpper := stripSQLTypePrecision(strings.ToUpper(before))
		afterUpper := strings.ToUpper(after)
		return (strings.HasSuffix(beforeUpper, "TIMESTAMP") || strings.HasSuffix(beforeUpper, "TIME")) &&
			(strings.HasPrefix(afterUpper, "TIME ZONE") || strings.HasPrefix(afterUpper, "LOCAL TIME ZONE"))
	case "SET":
		return source == "column type" &&
			(nextNonSpace(fragment, end) == '(' || isCharacterSetModifier(fragment, start))
	default:
		return false
	}
}

func isCharacterSetModifier(fragment string, setStart int) bool {
	before := strings.ToUpper(strings.TrimSpace(fragment[:setStart]))
	if before == "CHARACTER" || strings.HasSuffix(before, " CHARACTER") {
		return true
	}
	tokenStart := previousTokenStart(fragment, setStart)
	return strings.EqualFold(strings.TrimSpace(fragment[tokenStart:setStart]), "CHAR")
}

func previousTokenStart(fragment string, end int) int {
	i := end
	for i > 0 {
		switch fragment[i-1] {
		case ' ', '\t', '\n', '\r':
			i--
		default:
			goto scan
		}
	}
scan:
	for i > 0 && sqlscan.IsIdentPart(fragment[i-1]) {
		i--
	}
	return i
}

func nextNonSpace(fragment string, start int) byte {
	for i := start; i < len(fragment); i++ {
		switch fragment[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return fragment[i]
		}
	}
	return 0
}

func stripSQLTypePrecision(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, ")") {
		return value
	}
	open := strings.LastIndex(value, "(")
	if open < 0 {
		return value
	}
	for _, ch := range value[open+1 : len(value)-1] {
		if ch < '0' || ch > '9' {
			return value
		}
	}
	return strings.TrimSpace(value[:open])
}
