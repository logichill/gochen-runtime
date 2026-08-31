package diff

import (
	"fmt"
	"strings"

	"gochen-runtime/db/schema"
)

// Kind 表示 schema 变更类型。
type Kind string

const (
	// KindAddTable 表示新增表。
	KindAddTable Kind = "add_table"
	// KindAddColumn 表示新增列。
	KindAddColumn Kind = "add_column"
	// KindAddIndex 表示新增索引。
	KindAddIndex Kind = "add_index"
)

// Change 描述一条安全新增类 schema 变更。
type Change struct {
	Kind   Kind
	Table  string
	Column *schema.Column
	Index  *schema.Index
	Target *schema.Table
}

// Changes 是 schema 变更集合。
type Changes []Change

// DriftKind 表示已存在结构与期望结构不一致，但不会自动生成破坏性 SQL 的差异。
type DriftKind string

const (
	// DriftTable 表示当前库存在期望结构未声明的表。
	DriftTable DriftKind = "table"
	// DriftColumn 表示同名列的结构不一致。
	DriftColumn DriftKind = "column"
	// DriftIndex 表示同名索引的结构不一致。
	DriftIndex DriftKind = "index"
)

// Drift 描述一条需要人工 review 的 schema drift。
type Drift struct {
	Kind   DriftKind
	Table  string
	Name   string
	Detail string
}

// Drifts 是 schema drift 集合。
type Drifts []Drift

// Between 比较 current 与 desired，并只输出安全新增类变更。
func Between(current *schema.Schema, desired *schema.Schema) Changes {
	if desired == nil {
		return nil
	}

	var changes Changes
	for _, desiredTable := range desired.Tables {
		desiredTable = normalizeTable(desiredTable)
		if desiredTable.Name == "" {
			continue
		}

		currentTable, ok := current.FindTable(desiredTable.Schema, desiredTable.Name)
		if !ok {
			table := desiredTable
			changes = append(changes, Change{
				Kind:   KindAddTable,
				Table:  table.QualifiedName(),
				Target: &table,
			})
			continue
		}

		for _, desiredColumn := range desiredTable.Columns {
			desiredColumn = normalizeColumn(desiredColumn)
			if desiredColumn.Name == "" {
				continue
			}
			if _, ok := currentTable.FindColumn(desiredColumn.Name); ok {
				continue
			}
			column := desiredColumn
			changes = append(changes, Change{
				Kind:   KindAddColumn,
				Table:  desiredTable.QualifiedName(),
				Column: &column,
			})
		}

		for _, desiredIndex := range desiredTable.Indexes {
			desiredIndex = normalizeIndex(desiredIndex)
			if desiredIndex.Name == "" || desiredIndex.Unsupported {
				continue
			}
			if _, ok := currentTable.FindIndex(desiredIndex.Name); ok {
				continue
			}
			if hasSQLiteAutoIndexEquivalent(currentTable.Indexes, desiredIndex) {
				continue
			}
			index := desiredIndex
			changes = append(changes, Change{
				Kind:  KindAddIndex,
				Table: desiredTable.QualifiedName(),
				Index: &index,
			})
		}
	}
	return changes
}

// DetectDrifts 比较同名表/列/索引，并输出不会自动修复的结构差异。
func DetectDrifts(current *schema.Schema, desired *schema.Schema) Drifts {
	if current == nil || desired == nil {
		return nil
	}

	var drifts Drifts
	for _, currentTable := range current.Tables {
		currentTable = normalizeTable(currentTable)
		if currentTable.Name == "" {
			continue
		}
		desiredTable, ok := desired.FindTable(currentTable.Schema, currentTable.Name)
		if !ok {
			drifts = append(drifts, Drift{
				Kind:   DriftTable,
				Table:  currentTable.QualifiedName(),
				Name:   currentTable.QualifiedName(),
				Detail: "exists in current schema but not desired",
			})
			continue
		}
		desiredNormalized := normalizeTable(*desiredTable)
		for _, currentColumn := range currentTable.Columns {
			currentColumn = normalizeColumn(currentColumn)
			if currentColumn.Name == "" {
				continue
			}
			if _, ok := desiredNormalized.FindColumn(currentColumn.Name); !ok {
				drifts = append(drifts, Drift{
					Kind:   DriftColumn,
					Table:  currentTable.QualifiedName(),
					Name:   currentColumn.Name,
					Detail: "column exists in database but not in desired schema",
				})
			}
		}
		for _, currentIndex := range currentTable.Indexes {
			currentIndex = normalizeIndex(currentIndex)
			if currentIndex.Name == "" {
				continue
			}
			if currentIndex.Unsupported {
				drifts = append(drifts, Drift{
					Kind:   DriftIndex,
					Table:  currentTable.QualifiedName(),
					Name:   currentIndex.Name,
					Detail: "index exists in database but is not represented by the schema AST",
				})
				continue
			}
			if _, ok := desiredNormalized.FindIndex(currentIndex.Name); ok {
				continue
			}
			if isSQLiteAutoIndex(currentIndex.Name) && hasEquivalentIndex(desiredNormalized.Indexes, currentIndex) {
				continue
			}
			drifts = append(drifts, Drift{
				Kind:   DriftIndex,
				Table:  currentTable.QualifiedName(),
				Name:   currentIndex.Name,
				Detail: "exists in current schema but not desired",
			})
		}
	}

	for _, desiredTable := range desired.Tables {
		desiredTable = normalizeTable(desiredTable)
		if desiredTable.Name == "" {
			continue
		}
		currentTable, ok := current.FindTable(desiredTable.Schema, desiredTable.Name)
		if !ok {
			continue
		}
		for _, desiredColumn := range desiredTable.Columns {
			desiredColumn = normalizeColumn(desiredColumn)
			if desiredColumn.Name == "" {
				continue
			}
			currentColumn, ok := currentTable.FindColumn(desiredColumn.Name)
			if !ok {
				continue
			}
			if detail := columnDriftDetail(*currentColumn, desiredColumn); detail != "" {
				drifts = append(drifts, Drift{
					Kind:   DriftColumn,
					Table:  desiredTable.QualifiedName(),
					Name:   desiredColumn.Name,
					Detail: detail,
				})
			}
		}
		for _, desiredIndex := range desiredTable.Indexes {
			desiredIndex = normalizeIndex(desiredIndex)
			if desiredIndex.Name == "" || desiredIndex.Unsupported {
				continue
			}
			currentIndex, ok := currentTable.FindIndex(desiredIndex.Name)
			if !ok {
				continue
			}
			if currentIndex.Unsupported {
				continue
			}
			if detail := indexDriftDetail(*currentIndex, desiredIndex); detail != "" {
				drifts = append(drifts, Drift{
					Kind:   DriftIndex,
					Table:  desiredTable.QualifiedName(),
					Name:   desiredIndex.Name,
					Detail: detail,
				})
			}
		}
	}
	return drifts
}

// Messages 返回可直接写入 migration 草稿注释的 drift 描述。
func (d Drifts) Messages() []string {
	messages := make([]string, 0, len(d))
	for _, drift := range d {
		if drift.Detail == "" {
			continue
		}
		if drift.Kind == DriftTable {
			messages = append(messages, fmt.Sprintf("Schema drift requires manual review: %s %s %s", drift.Kind, drift.Table, drift.Detail))
			continue
		}
		messages = append(messages, fmt.Sprintf("Schema drift requires manual review: %s %s.%s %s", drift.Kind, drift.Table, drift.Name, drift.Detail))
	}
	return messages
}

func normalizeTable(table schema.Table) schema.Table {
	if strings.TrimSpace(table.Schema) == "" {
		table.Schema, table.Name = schema.ParseTableName(table.Name)
	} else {
		table.Schema = strings.TrimSpace(table.Schema)
		table.Name = strings.TrimSpace(table.Name)
	}
	for i := range table.Columns {
		table.Columns[i] = normalizeColumn(table.Columns[i])
	}
	for i := range table.Indexes {
		table.Indexes[i] = normalizeIndex(table.Indexes[i])
	}
	return table
}

func normalizeColumn(column schema.Column) schema.Column {
	column.Name = strings.TrimSpace(column.Name)
	column.Type = schema.NormalizeColumnType(column.Type)
	column.DefaultValue = schema.NormalizeDefaultValue(column.Type, column.DefaultValue, "")
	if column.AutoIncrement && isGeneratedAutoIncrementDefault(column.DefaultValue) {
		column.DefaultValue = ""
	}
	return column
}

func normalizeIndex(index schema.Index) schema.Index {
	index.Name = strings.TrimSpace(index.Name)
	for i := range index.Columns {
		index.Columns[i] = strings.TrimSpace(index.Columns[i])
	}
	return index
}

func hasEquivalentIndex(indexes []schema.Index, target schema.Index) bool {
	target = normalizeIndex(target)
	for _, index := range indexes {
		index = normalizeIndex(index)
		if index.Unsupported {
			continue
		}
		if index.Unique != target.Unique || len(index.Columns) != len(target.Columns) {
			continue
		}
		matched := true
		for i := range target.Columns {
			if index.Columns[i] != target.Columns[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func hasSQLiteAutoIndexEquivalent(indexes []schema.Index, target schema.Index) bool {
	for _, index := range indexes {
		if !isSQLiteAutoIndex(index.Name) {
			continue
		}
		if hasEquivalentIndex([]schema.Index{index}, target) {
			return true
		}
	}
	return false
}

func isSQLiteAutoIndex(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "sqlite_autoindex_")
}

func columnDriftDetail(current schema.Column, desired schema.Column) string {
	current = normalizeColumn(current)
	desired = normalizeColumn(desired)
	var parts []string
	if !strings.EqualFold(current.Type, desired.Type) {
		parts = append(parts, fmt.Sprintf("type current=%q desired=%q", current.Type, desired.Type))
	}
	if current.Nullable != desired.Nullable {
		parts = append(parts, fmt.Sprintf("nullable current=%t desired=%t", current.Nullable, desired.Nullable))
	}
	if strings.TrimSpace(current.DefaultValue) != strings.TrimSpace(desired.DefaultValue) {
		parts = append(parts, fmt.Sprintf("default current=%q desired=%q", current.DefaultValue, desired.DefaultValue))
	}
	if current.PrimaryKey != desired.PrimaryKey {
		parts = append(parts, fmt.Sprintf("primary_key current=%t desired=%t", current.PrimaryKey, desired.PrimaryKey))
	}
	if current.AutoIncrement != desired.AutoIncrement {
		parts = append(parts, fmt.Sprintf("auto_increment current=%t desired=%t", current.AutoIncrement, desired.AutoIncrement))
	}
	return strings.Join(parts, "; ")
}

func indexDriftDetail(current schema.Index, desired schema.Index) string {
	current = normalizeIndex(current)
	desired = normalizeIndex(desired)
	var parts []string
	if current.Unique != desired.Unique {
		parts = append(parts, fmt.Sprintf("unique current=%t desired=%t", current.Unique, desired.Unique))
	}
	if !sameStringSlice(current.Columns, desired.Columns) {
		parts = append(parts, fmt.Sprintf("columns current=%q desired=%q", current.Columns, desired.Columns))
	}
	return strings.Join(parts, "; ")
}

func sameStringSlice(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

func isGeneratedAutoIncrementDefault(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "nextval(")
}
