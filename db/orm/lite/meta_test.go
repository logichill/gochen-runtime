package lite

import (
	"database/sql/driver"
	"reflect"
	"testing"
)

type scannerMetaList []string

func (*scannerMetaList) Scan(any) error {
	return nil
}

func (scannerMetaList) Value() (driver.Value, error) {
	return "[]", nil
}

type valuerOnlyMetaField struct {
	raw string
}

func (v valuerOnlyMetaField) Value() (driver.Value, error) {
	return v.raw, nil
}

type scannerOnlyMetaField struct {
	raw string
}

func (*scannerOnlyMetaField) Scan(any) error {
	return nil
}

type EmbeddedScannerValuerMetaField struct {
	raw string
}

func (*EmbeddedScannerValuerMetaField) Scan(any) error {
	return nil
}

func (v EmbeddedScannerValuerMetaField) Value() (driver.Value, error) {
	return v.raw, nil
}

func TestBuildStructMeta_SnakeCaseHandlesInitialisms(t *testing.T) {
	type scopedEntity struct {
		ID             int64
		ManagedScopeID int64
		OwnerID        string
		TenantID       string
		HTTPStatus     int
	}

	meta := buildStructMeta(reflectTypeOf[scopedEntity]())

	for _, column := range []string{"id", "managed_scope_id", "owner_id", "tenant_id", "http_status"} {
		if _, ok := meta.columnToInfo[column]; !ok {
			t.Fatalf("expected column %q in %#v", column, meta.columnToInfo)
		}
	}
	if _, ok := meta.columnToInfo["managed_scope_i_d"]; ok {
		t.Fatalf("did not expect legacy split initialism column")
	}
}

func TestBuildStructMeta_IncludesScannerValuerFields(t *testing.T) {
	type roleEntity struct {
		ID          int64
		Permissions scannerMetaList `json:"permissions" gorm:"type:text;serializer:json"`
		ParentKey   int64           `json:"-" gorm:"not null;default:0;index"`
		Transient   string          `gorm:"-"`
		Hidden      string          `json:"-"`
		Raw         []string
	}

	meta := buildStructMeta(reflectTypeOf[roleEntity]())

	if _, ok := meta.columnToInfo["permissions"]; !ok {
		t.Fatalf("expected scanner/valuer permissions column in %#v", meta.columnToInfo)
	}
	if _, ok := meta.columnToInfo["parent_key"]; !ok {
		t.Fatalf("expected gorm-tagged json-hidden parent_key column in %#v", meta.columnToInfo)
	}
	for _, column := range []string{"transient", "hidden", "raw"} {
		if _, ok := meta.columnToInfo[column]; ok {
			t.Fatalf("did not expect column %q in %#v", column, meta.columnToInfo)
		}
	}
}

func TestBuildStructMeta_ValuerOnlyFieldsAreWriteOnly(t *testing.T) {
	type writeOnlyEntity struct {
		ID      int64
		Payload valuerOnlyMetaField `json:"payload" gorm:"type:text"`
	}

	meta := buildStructMeta(reflectTypeOf[writeOnlyEntity]())

	cols, _ := meta.insertableColumns()
	if !containsColumn(cols, "payload") {
		t.Fatalf("expected valuer-only payload to be insertable, got %#v", cols)
	}
	if _, ok := meta.columnToInfo["payload"]; ok {
		t.Fatalf("did not expect valuer-only payload to be used as scan target in %#v", meta.columnToInfo)
	}
}

func TestBuildStructMeta_ScannerOnlyFieldsAreReadOnly(t *testing.T) {
	type readOnlyEntity struct {
		ID      int64
		Payload scannerOnlyMetaField `json:"payload" gorm:"type:text"`
	}

	meta := buildStructMeta(reflectTypeOf[readOnlyEntity]())

	cols, _ := meta.insertableColumns()
	if containsColumn(cols, "payload") {
		t.Fatalf("did not expect scanner-only payload to be insertable, got %#v", cols)
	}
	if _, ok := meta.columnToInfo["payload"]; !ok {
		t.Fatalf("expected scanner-only payload to be usable as scan target in %#v", meta.columnToInfo)
	}
}

func TestBuildStructMeta_GormTagWithoutColumnPreservesJSONName(t *testing.T) {
	type taggedEntity struct {
		DisplayName  string `json:"display" gorm:"size:64"`
		CustomColumn string `json:"api_name" gorm:"column:db_name;size:64"`
		JSONHidden   string `json:"-" gorm:"not null"`
		JSONOnly     string `json:"json_only"`
	}

	meta := buildStructMeta(reflectTypeOf[taggedEntity]())

	for _, column := range []string{"display", "db_name", "json_hidden", "json_only"} {
		if _, ok := meta.columnToInfo[column]; !ok {
			t.Fatalf("expected column %q in %#v", column, meta.columnToInfo)
		}
	}
	for _, column := range []string{"display_name", "api_name"} {
		if _, ok := meta.columnToInfo[column]; ok {
			t.Fatalf("did not expect column %q in %#v", column, meta.columnToInfo)
		}
	}
}

func TestBuildStructMeta_SkipsIgnoredEmbeddedStruct(t *testing.T) {
	type IgnoredAudit struct {
		Secret string
	}
	type entity struct {
		IgnoredAudit `gorm:"-"`
		ID           int64
	}

	meta := buildStructMeta(reflectTypeOf[entity]())

	if _, ok := meta.columnToInfo["id"]; !ok {
		t.Fatalf("expected id column in %#v", meta.columnToInfo)
	}
	if _, ok := meta.columnToInfo["secret"]; ok {
		t.Fatalf("did not expect ignored embedded secret column in %#v", meta.columnToInfo)
	}
}

func TestBuildStructMeta_ExpandsPointerEmbeddedStruct(t *testing.T) {
	type BaseEntity struct {
		ID int64
	}
	type entity struct {
		*BaseEntity
		Name string
	}

	meta := buildStructMeta(reflectTypeOf[entity]())

	for _, column := range []string{"id", "name"} {
		if _, ok := meta.columnToInfo[column]; !ok {
			t.Fatalf("expected column %q in %#v", column, meta.columnToInfo)
		}
	}
}

func TestBuildStructMeta_AnonymousScannerValuerFieldIsColumn(t *testing.T) {
	type entity struct {
		ID                             int64
		EmbeddedScannerValuerMetaField `gorm:"column:payload"`
	}

	meta := buildStructMeta(reflectTypeOf[entity]())

	cols, _ := meta.insertableColumns()
	if !containsColumn(cols, "payload") {
		t.Fatalf("expected anonymous scanner/valuer payload to be insertable, got %#v", cols)
	}
	if _, ok := meta.columnToInfo["payload"]; !ok {
		t.Fatalf("expected anonymous scanner/valuer payload column in %#v", meta.columnToInfo)
	}
	if _, ok := meta.columnToInfo["raw"]; ok {
		t.Fatalf("did not expect anonymous scanner/valuer internals to be expanded: %#v", meta.columnToInfo)
	}
}

func reflectTypeOf[T any]() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}

func containsColumn(cols []string, want string) bool {
	for _, col := range cols {
		if col == want {
			return true
		}
	}
	return false
}
