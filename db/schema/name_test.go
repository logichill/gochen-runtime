package schema

import "testing"

func TestParseTableName(t *testing.T) {
	tests := []struct {
		name       string
		wantSchema string
		wantTable  string
	}{
		{name: "", wantSchema: "", wantTable: ""},
		{name: "users", wantSchema: "", wantTable: "users"},
		{name: " public.users ", wantSchema: "public", wantTable: "users"},
		{name: "catalog.schema.table", wantSchema: "catalog.schema", wantTable: "table"},
		{name: ".users", wantSchema: "", wantTable: "users"},
		{name: "public.", wantSchema: "public", wantTable: ""},
		{name: " public . users ", wantSchema: "public", wantTable: "users"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schemaName, tableName := ParseTableName(tt.name)
			if schemaName != tt.wantSchema || tableName != tt.wantTable {
				t.Fatalf("expected %q/%q, got %q/%q", tt.wantSchema, tt.wantTable, schemaName, tableName)
			}
		})
	}
}
