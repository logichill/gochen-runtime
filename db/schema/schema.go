package schema

import "strings"

// Schema 表示数据库结构快照。
type Schema struct {
	Tables   []Table
	Warnings []string
}

// Table 表示单张数据表结构。
type Table struct {
	Schema  string
	Name    string
	Columns []Column
	Indexes []Index
}

// Column 表示单个列结构。
type Column struct {
	Name          string
	Type          string
	Nullable      bool
	DefaultValue  string
	PrimaryKey    bool
	AutoIncrement bool
}

// Index 表示普通/唯一索引结构。
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	// Unsupported 表示 introspect 确认索引存在，但当前 AST 无法安全表达其完整定义。
	Unsupported bool
}

// FindTable 按 schema 与表名查找表结构。
func (s *Schema) FindTable(schemaName string, name string) (*Table, bool) {
	if s == nil {
		return nil, false
	}
	schemaName = strings.TrimSpace(schemaName)
	name = strings.TrimSpace(name)
	for i := range s.Tables {
		tableSchema := strings.TrimSpace(s.Tables[i].Schema)
		tableName := strings.TrimSpace(s.Tables[i].Name)
		if tableSchema == "" {
			tableSchema, tableName = ParseTableName(tableName)
		}
		if tableSchema == schemaName && tableName == name {
			return &s.Tables[i], true
		}
	}
	return nil, false
}

// QualifiedName 返回表的限定名；无 schema 时返回裸表名。
func (t Table) QualifiedName() string {
	if strings.TrimSpace(t.Schema) == "" {
		return strings.TrimSpace(t.Name)
	}
	if strings.TrimSpace(t.Name) == "" {
		return strings.TrimSpace(t.Schema)
	}
	return strings.TrimSpace(t.Schema) + "." + strings.TrimSpace(t.Name)
}

// FindColumn 按列名查找列结构。
func (t *Table) FindColumn(name string) (*Column, bool) {
	if t == nil {
		return nil, false
	}
	name = strings.TrimSpace(name)
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i], true
		}
	}
	return nil, false
}

// FindIndex 按索引名查找索引结构。
func (t *Table) FindIndex(name string) (*Index, bool) {
	if t == nil {
		return nil, false
	}
	name = strings.TrimSpace(name)
	for i := range t.Indexes {
		if t.Indexes[i].Name == name {
			return &t.Indexes[i], true
		}
	}
	return nil, false
}
