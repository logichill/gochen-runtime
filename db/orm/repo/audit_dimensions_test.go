package repo_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	repopkg "gochen-runtime/db/orm/repo"
	"gochen/app"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/testkit"
	"gochen/testkit/contracttest"
)

func TestAuditStoreFieldNamesDoNotCollideWithBusinessBindings(t *testing.T) {
	for _, identity := range []string{"AuditID", "AuditAction", "AuditEntityID", "AuditDimensions", "AuditBaseID", "BusinessID"} {
		t.Run(identity, func(t *testing.T) {
			engine := newEngine(t)
			execSQL(t, engine, `CREATE TABLE collision_audit (
				id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL,
				operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL,
				timestamp DATETIME NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL, department_id INTEGER NOT NULL)`)
			dimensionField := "Department"
			if identity == "BusinessID" {
				dimensionField = "AuditOperator"
			}
			rowType := reflect.StructOf([]reflect.StructField{
				{Name: identity, Type: reflect.TypeOf(int64(0)), Tag: `entity:"id"`},
				{Name: dimensionField, Type: reflect.TypeOf(int64(0)), Tag: `dim:"department"`},
			})
			metadata, err := meta.DescribeType(reflect.PointerTo(rowType))
			requireNoError(t, err, "business metadata")
			execution, err := app.BindContract(metadata, app.ResourceContract{})
			requireNoError(t, err, "contract")
			store, err := repopkg.NewAuditStore(engine, "collision_audit", &counterGen{}, repopkg.AuditResource{
				Resource: "collision", Execution: execution, Columns: map[meta.Dimension]string{"department": "department_id"},
			})
			requireNoError(t, err, "constructor")
			value := reflect.New(rowType)
			value.Elem().FieldByName(identity).SetInt(42)
			value.Elem().FieldByName(dimensionField).SetInt(7)
			dimensions, err := audited.CaptureDimensions(metadata, value.Interface())
			requireNoError(t, err, "dimensions")
			ctx := audited.WithAuditResourceKind(context.Background(), "collision")
			_, err = store.SaveAuditRecord(ctx, audited.AuditRecord{ResourceKind: "collision", EntityID: "42", Action: "update", Operator: "alice", OperationID: "op", Dimensions: dimensions})
			requireNoError(t, err, "save")
			for _, ref := range []meta.Ref{meta.ID(), meta.ByField(identity)} {
				records, err := store.ListAuditRecords(ctx, "42", 0, 10, spec.And(spec.Eq(ref, 42), spec.Eq(meta.ByField(dimensionField), 7)))
				requireNoError(t, err, "read")
				if len(records) != 1 || records[0].Operator != "alice" || records[0].EntityID != "42" {
					t.Fatalf("round trip=%+v", records)
				}
			}
		})
	}
}

func TestAuditIdentityConversionMatchesMemory(t *testing.T) {
	r, engine := newDimensionSQL(t)
	sqlStore := newDimensionAudit(t, r, engine)
	memory, err := testkit.NewAuditedMemoryRepository[*contracttest.Document, int64](nil,
		testkit.WithMemoryResourceContract[*contracttest.Document, int64](contracttest.Contract()),
		testkit.WithMemoryIsolationField[*contracttest.Document, int64](meta.ByDimension("tenant"), nil))
	requireNoError(t, err, "memory")
	ctx, err := contextx.WithTenantID(context.Background(), "t1")
	requireNoError(t, err, "tenant")
	ctx = audited.WithAuditResourceKind(ctx, "document")
	metadata, err := meta.Describe[*contracttest.Document]()
	requireNoError(t, err, "metadata")
	snapshot, err := audited.CaptureDimensions(metadata, &contracttest.Document{Tenant: "t1", Department: 7})
	requireNoError(t, err, "snapshot")
	record := audited.AuditRecord{ResourceKind: "document", EntityID: "42", Action: "update", Operator: "alice", OperationID: "op", Dimensions: snapshot}
	for _, store := range []audited.IAuditStore{sqlStore, memory} {
		_, err := store.SaveAuditRecord(ctx, record)
		requireNoError(t, err, "save")
	}
	for _, ref := range []meta.Ref{meta.ID(), meta.ByField("ID")} {
		for _, condition := range []spec.Spec{spec.Eq(ref, "0042"), spec.In(ref, "0042", "0043"), spec.Eq(ref, "invalid"), spec.Eq(ref, "9223372036854775808")} {
			want, wantErr := memory.ListAuditRecords(ctx, "42", 0, 10, condition)
			got, gotErr := sqlStore.ListAuditRecords(ctx, "42", 0, 10, condition)
			if len(got) != len(want) || errors.Code(gotErr) != errors.Code(wantErr) {
				t.Errorf("%s: SQL count=%d error=%v, memory count=%d error=%v", condition.Canonical(), len(got), gotErr, len(want), wantErr)
			}
		}
	}
}

func newDimensionAudit(t *testing.T, repository *repopkg.Repo[*contracttest.Document, int64], engine orm.IOrm) *repopkg.AuditStore {
	t.Helper()
	execSQL(t, engine, `CREATE TABLE audit_records (
		id INTEGER PRIMARY KEY, resource_kind TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL,
		operator TEXT NOT NULL, initiator TEXT, reason TEXT, correlation_id TEXT, operation_id TEXT NOT NULL,
		timestamp DATETIME NOT NULL, changes TEXT, metadata TEXT, dimensions TEXT NOT NULL,
		tenant_id TEXT NOT NULL, department_id INTEGER NOT NULL, region_code TEXT, classification BOOLEAN NOT NULL)`,
		`CREATE UNIQUE INDEX transfer_pair ON audit_records(operation_id, resource_kind, entity_id, action) WHERE action IN ('transfer_out', 'transfer_in')`)
	config := repository.AuditResource(map[meta.Dimension]string{"tenant": "tenant_id", "department": "department_id", "region": "region_code", "classification": "classification"})
	config.IsolationColumn = "tenant_id"
	store, err := repopkg.NewAuditStore(engine, "audit_records", &counterGen{}, config)
	requireNoError(t, err, "NewAuditStore")
	return store
}

func TestAuditStoreD11FieldsAndSpecRestriction(t *testing.T) {
	r, engine := newDimensionSQL(t)
	store := newDimensionAudit(t, r, engine)
	ctx, err := contextx.WithTenantID(context.Background(), "t1")
	requireNoError(t, err, "tenant")
	ctx = audited.WithAuditResourceKind(ctx, "document")
	metadata, err := meta.Describe[*contracttest.Document]()
	requireNoError(t, err, "metadata")
	region := "north"
	dimensions, err := audited.CaptureDimensions(metadata, &contracttest.Document{Tenant: "t1", Department: 7, Region: &region, Classified: true})
	requireNoError(t, err, "dimensions")
	timestamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := audited.AuditRecord{ResourceKind: "document", EntityID: "42", Action: "submit", Operator: "alice", Initiator: "bob",
		Reason: "month-end close", CorrelationID: "corr-1", OperationID: "op-1", Timestamp: timestamp, Dimensions: dimensions,
		Changes: []byte(`[{"field":"name","old":"a","new":"b"}]`), Metadata: []byte(`{"source":"test"}`)}
	id, err := store.SaveAuditRecord(ctx, record)
	requireNoError(t, err, "save")
	if id == 0 {
		t.Fatal("id was not generated")
	}
	for _, filter := range []spec.Spec{
		spec.Eq(meta.ByDimension("department"), "7"),
		spec.And(spec.Eq(meta.ByField("Department"), 7), spec.In(meta.ByDimension("region"), "north", "south")),
		spec.Eq(meta.ID(), 42),
	} {
		records, err := store.ListAuditRecords(ctx, "42", 0, 10, filter)
		requireNoError(t, err, "read")
		if len(records) != 1 {
			t.Fatalf("records = %+v", records)
		}
		actual := records[0]
		if actual.Action != record.Action || actual.Operator != record.Operator || actual.Initiator != record.Initiator || actual.Reason != record.Reason || actual.CorrelationID != record.CorrelationID || actual.OperationID != "op-1" || !actual.Timestamp.Equal(timestamp) || !reflect.DeepEqual(actual.Dimensions.Values(), dimensions.Values()) || !reflect.DeepEqual(actual.Changes, record.Changes) {
			t.Fatalf("round trip: %+v", actual)
		}
	}
	for _, filter := range []spec.Spec{spec.Eq(meta.ByField("Name"), "a"), spec.Named("active"), spec.Eq(meta.ByDimension("classification"), true), spec.Eq(meta.ByField("Tenant"), "t1")} {
		_, err := store.ListAuditRecords(ctx, "42", 0, 10, filter)
		requireCode(t, err, errors.Internal, "restricted audit filter")
	}
	otherCtx, err := contextx.WithTenantID(ctx, "t2")
	requireNoError(t, err, "other tenant")
	empty, err := store.ListAuditRecords(otherCtx, "42", 0, 10, spec.All())
	requireNoError(t, err, "isolation")
	if len(empty) != 0 {
		t.Fatal("cross-tenant audit leak")
	}
	bad := record
	bad.OperationID = ""
	_, err = store.SaveAuditRecord(ctx, bad)
	requireCode(t, err, errors.InvalidInput, "operation required")
	bad = record
	bad.Dimensions = audited.DimensionSnapshot{}
	_, err = store.SaveAuditRecord(ctx, bad)
	requireCode(t, err, errors.InvalidInput, "complete snapshot required")
	bad = record
	bad.Operator = ""
	err = store.SaveAuditRecords(ctx, []audited.AuditRecord{record, bad})
	requireCode(t, err, errors.InvalidInput, "bulk prevalidation")
	_, err = engine.Database().Exec(ctx, `UPDATE audit_records SET dimensions = '{}' WHERE id = ?`, id)
	requireNoError(t, err, "simulate old incomplete snapshot")
	_, err = store.ListAuditRecords(ctx, "42", 0, 10, spec.All())
	requireCode(t, err, errors.Internal, "missing historical key")
}

func TestAuditBatchMultipleInsertsRequiresTransactionAndRollsBack(t *testing.T) {
	r, engine := newDimensionSQL(t)
	newDimensionAudit(t, r, engine)
	columns := map[meta.Dimension]string{"tenant": "tenant_id", "department": "department_id", "region": "region_code", "classification": "classification"}
	first := r.AuditResource(columns)
	first.IsolationColumn = "tenant_id"
	second := first
	second.Resource = "another_document"
	store, err := repopkg.NewAuditStore(engine, "audit_records", &counterGen{}, first, second)
	requireNoError(t, err, "store")
	ctx, err := contextx.WithTenantID(context.Background(), "t1")
	requireNoError(t, err, "tenant")
	metadata, err := meta.Describe[*contracttest.Document]()
	requireNoError(t, err, "metadata")
	snapshot, err := audited.CaptureDimensions(metadata, &contracttest.Document{Tenant: "t1", Department: 7})
	requireNoError(t, err, "snapshot")
	record := audited.AuditRecord{ResourceKind: "document", EntityID: "42", Action: "update", Operator: "alice", OperationID: "op", Dimensions: snapshot}
	other := record
	other.ResourceKind = second.Resource
	records := []audited.AuditRecord{record, other}
	requireCode(t, store.SaveAuditRecords(ctx, records), errors.FailedPrecondition, "multi-insert transaction required")
	execSQL(t, engine, `CREATE TRIGGER reject_second_audit BEFORE INSERT ON audit_records WHEN NEW.resource_kind = 'another_document' BEGIN SELECT RAISE(ABORT, 'second audit failed'); END`)
	err = r.WithinTx(ctx, func(txCtx context.Context) error { return store.SaveAuditRecords(txCtx, records) })
	if err == nil {
		t.Fatal("expected second insert failure")
	}
	readCtx := audited.WithAuditResourceKind(ctx, "document")
	stored, err := store.ListAuditRecords(readCtx, "42", 0, 10, spec.All())
	requireNoError(t, err, "read rollback")
	if len(stored) != 0 {
		t.Fatalf("partial audit batch=%+v", stored)
	}
}
