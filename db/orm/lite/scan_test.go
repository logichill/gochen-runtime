package lite

import (
	"database/sql"
	"database/sql/driver"
	"testing"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"

	_ "modernc.org/sqlite"
)

type scanTestEntity struct {
	ID   int64
	Name string
}

type pointerValuerWriteField struct {
	raw string
}

func (v *pointerValuerWriteField) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return "ptr:" + v.raw, nil
}

func (scanTestEntity) TableName() string {
	return "scan_test_entities"
}

func TestScanRowsIntoDestSupportsPointerTargets(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(nil)
	if err == nil || model != nil {
		t.Fatalf("expected nil model meta to fail")
	}

	model, err = liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var one *scanTestEntity
	if err := model.First(t.Context(), &one); err != nil {
		t.Fatalf("First into pointer: %v", err)
	}
	if one == nil || one.ID != 1 || one.Name != "alpha" {
		t.Fatalf("unexpected first entity: %#v", one)
	}

	var many []*scanTestEntity
	if err := model.Find(t.Context(), &many); err != nil {
		t.Fatalf("Find into pointer slice: %v", err)
	}
	if len(many) != 2 || many[0] == nil || many[1] == nil {
		t.Fatalf("unexpected pointer slice: %#v", many)
	}
	if many[0].Name != "alpha" || many[1].Name != "beta" {
		t.Fatalf("unexpected names: %#v", many)
	}
}

func TestFindSupportsSingleStructProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var row struct {
		MaxID int64 `gorm:"column:max_id"`
	}
	if err := model.Find(t.Context(), &row, orm.WithSelectExprUnsafe("MAX(id) AS max_id")); err != nil {
		t.Fatalf("Find into struct projection: %v", err)
	}
	if row.MaxID != 2 {
		t.Fatalf("unexpected max id: %d", row.MaxID)
	}
}

func TestFindSingleStructProjectionNoRowsReturnsNotFound(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var row scanTestEntity
	err = model.Find(t.Context(), &row, orm.WithWhere("id = ?", int64(999)))
	if !errors.Is(err, errors.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestFindMixedSafeColumnAndRawExprValidatesSafeColumn(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	// 安全列与原始表达式混用：安全列经校验并 quote 后与 raw 合并，查询正常返回。
	var rows []scanTestEntity
	if err := model.Find(t.Context(), &rows,
		orm.WithSelect("name"),
		orm.WithSelectExprUnsafe("id"),
		orm.WithOrderBy("id", false),
	); err != nil {
		t.Fatalf("mixed safe+raw select: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[0].Name != "alpha" {
		t.Fatalf("unexpected rows: %#v", rows)
	}

	// 不安全的 qo.Select 列即便与原始表达式混用也必须被拒绝，不得借 raw 通道绕过标识符校验。
	var sink []scanTestEntity
	err = model.Find(t.Context(), &sink,
		orm.WithSelect("name; DROP TABLE scan_test_entities"),
		orm.WithSelectExprUnsafe("id"),
	)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for unsafe mixed select column, got %v", err)
	}
}

func TestFindSupportsScalarSliceProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var ids []int64
	if err := model.Find(t.Context(), &ids, orm.WithSelect("id"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("Find into scalar slice: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestFindSupportsScalarPointerSliceProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var ids []*int64
	if err := model.Find(t.Context(), &ids, orm.WithSelect("id"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("Find into scalar pointer slice: %v", err)
	}
	if len(ids) != 2 || ids[0] == nil || ids[1] == nil {
		t.Fatalf("unexpected ids: %#v", ids)
	}
	if *ids[0] != 1 || *ids[1] != 2 {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestFindSupportsScalarPointerProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var name *string
	if err := model.Find(t.Context(), &name, orm.WithSelect("name"), orm.WithWhere("id = ?", int64(1))); err != nil {
		t.Fatalf("Find into scalar pointer: %v", err)
	}
	if name == nil || *name != "alpha" {
		t.Fatalf("unexpected scalar pointer: %#v", name)
	}
}

func TestFindScalarPointerProjectionKeepsNullNil(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var name *string
	if err := model.Find(t.Context(), &name, orm.WithSelectExprUnsafe("NULL AS name")); err != nil {
		t.Fatalf("Find into nullable scalar pointer: %v", err)
	}
	if name != nil {
		t.Fatalf("expected nil scalar pointer, got %#v", name)
	}
}

func TestFindScalarPointerSliceKeepsNullValuesNil(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var names []*string
	if err := model.Find(t.Context(), &names,
		orm.WithSelectExprUnsafe("CASE WHEN id = 2 THEN NULL ELSE name END AS name"),
		orm.WithOrderBy("id", false),
	); err != nil {
		t.Fatalf("Find into nullable scalar pointer slice: %v", err)
	}
	if len(names) != 2 || names[0] == nil || names[1] != nil {
		t.Fatalf("unexpected nullable names: %#v", names)
	}
	if *names[0] != "alpha" {
		t.Fatalf("unexpected nullable names: %#v", names)
	}
}

func TestFirstSupportsScannerScalarProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var name sql.NullString
	if err := model.First(t.Context(), &name, orm.WithSelect("name"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("First into sql.NullString: %v", err)
	}
	if !name.Valid || name.String != "alpha" {
		t.Fatalf("unexpected sql.NullString: %#v", name)
	}
}

func TestFirstSupportsPointerScannerStructField(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var row struct {
		Name *sql.NullString `gorm:"column:name"`
	}
	if err := model.First(t.Context(), &row, orm.WithSelect("name"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("First into pointer scanner field: %v", err)
	}
	if row.Name == nil || !row.Name.Valid || row.Name.String != "alpha" {
		t.Fatalf("unexpected pointer scanner field: %#v", row.Name)
	}
}

func TestFirstAllocatesPointerEmbeddedStruct(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	type EmbeddedBase struct {
		ID int64 `gorm:"column:id"`
	}
	type rowEntity struct {
		*EmbeddedBase
		Name string
	}

	var row rowEntity
	if err := model.First(t.Context(), &row, orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("First into pointer embedded struct: %v", err)
	}
	if row.EmbeddedBase == nil {
		t.Fatal("expected embedded base to be allocated")
	}
	if row.ID != 1 || row.Name != "alpha" {
		t.Fatalf("unexpected row: %#v", row)
	}
}

func TestFirstScalarProjectionRejectsMultipleColumns(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var id int64
	err = model.First(t.Context(), &id, orm.WithSelect("id", "name"))
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestFindNilDestReturnsInvalidInput(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	err = model.Find(t.Context(), nil)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestFindSupportsScannerPointerSliceProjection(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var names []*sql.NullString
	if err := model.Find(t.Context(), &names, orm.WithSelect("name"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("Find into scanner pointer slice: %v", err)
	}
	if len(names) != 2 || names[0] == nil || names[1] == nil {
		t.Fatalf("unexpected scanner pointer slice: %#v", names)
	}
	if !names[0].Valid || !names[1].Valid || names[0].String != "alpha" || names[1].String != "beta" {
		t.Fatalf("unexpected scanner values: %#v", names)
	}
}

func TestCreateUsesAddressForPointerReceiverValuerFields(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	sqlDB, ok := stdsql.DBOf(liteORM.Database())
	if !ok {
		t.Fatalf("expected stdsql database")
	}
	execScanTestSQL(t, sqlDB, `CREATE TABLE pointer_valuer_entities (id INTEGER PRIMARY KEY, payload TEXT)`)

	type pointerValuerEntity struct {
		ID      int64
		Payload pointerValuerWriteField `json:"payload" gorm:"type:text"`
	}

	model, err := liteORM.Model(&orm.ModelMeta{
		ModelFactory: orm.NewModelFactory[pointerValuerEntity](),
		Table:        "pointer_valuer_entities",
	})
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	if err := model.Create(t.Context(), pointerValuerEntity{
		ID:      1,
		Payload: pointerValuerWriteField{raw: "ok"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got string
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT payload FROM pointer_valuer_entities WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("query payload: %v", err)
	}
	if got != "ptr:ok" {
		t.Fatalf("unexpected payload: %q", got)
	}
}

func TestFirstSupportsScalarDest(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	var id int64
	if err := model.First(t.Context(), &id, orm.WithSelect("id"), orm.WithOrderBy("id", false)); err != nil {
		t.Fatalf("First into scalar &int64: %v", err)
	}
	if id != 1 {
		t.Fatalf("unexpected id: %d", id)
	}
}

func TestFindIgnoresUnknownColumns(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	// Projection includes a column ("name") that is not mapped in the narrow struct.
	// It should be silently discarded without error.
	var row struct {
		ID int64 `gorm:"column:id"`
	}
	if err := model.Find(t.Context(), &row,
		orm.WithSelect("id", "name"),
		orm.WithWhere("id = ?", int64(1)),
	); err != nil {
		t.Fatalf("Find with unknown column: %v", err)
	}
	if row.ID != 1 {
		t.Fatalf("unexpected id: %d", row.ID)
	}
}

func TestCreateRejectsMixedEntityTypes(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	type otherEntity struct {
		ID    int64
		Extra string
	}

	err = model.Create(t.Context(),
		scanTestEntity{ID: 10, Name: "first"},
		otherEntity{ID: 11, Extra: "second"},
	)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for mixed entity types, got %v", err)
	}
}

func TestCreateRejectsNonStructEntity(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	err = model.Create(t.Context(), "not-a-struct")
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for non-struct entity, got %v", err)
	}
}

func TestParseColumnTagTrimsColumnValue(t *testing.T) {
	type entity struct {
		Payload string `gorm:"column: payload_col ;type:text"`
	}

	meta := buildStructMeta(reflectTypeOf[entity]())

	if _, ok := meta.columnToInfo["payload_col"]; !ok {
		t.Fatalf("expected column 'payload_col' after trimming spaces; got %#v", meta.columnToInfo)
	}
	if _, ok := meta.columnToInfo[" payload_col "]; ok {
		t.Fatalf("column name must not contain surrounding spaces: %#v", meta.columnToInfo)
	}
}

func setupScanTestOrm(t *testing.T) *Orm {
	t.Helper()

	database, err := stdsql.NewWithContext(t.Context(), db.DBConfig{
		Driver:   "sqlite",
		Database: "file:" + t.TempDir() + "/scan.db",
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}
	})

	sqlDB, ok := stdsql.DBOf(database)
	if !ok {
		t.Fatalf("expected stdsql database")
	}
	execScanTestSQL(t, sqlDB, `CREATE TABLE scan_test_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	execScanTestSQL(t, sqlDB, `INSERT INTO scan_test_entities (id, name) VALUES (1, 'alpha'), (2, 'beta')`)

	ormEngine, err := New(database)
	if err != nil {
		t.Fatalf("New orm: %v", err)
	}
	typed, ok := ormEngine.(*Orm)
	if !ok {
		t.Fatalf("expected lite orm")
	}
	return typed
}

func execScanTestSQL(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

func modelMetaForScanTest() *orm.ModelMeta {
	return &orm.ModelMeta{
		ModelFactory: orm.NewModelFactory[scanTestEntity](),
		Table:        "scan_test_entities",
	}
}
