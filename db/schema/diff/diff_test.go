package diff

import (
	"testing"

	"gochen-runtime/db/schema"
)

func TestBetweenProducesSafeAdditiveChanges(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
			Indexes: []schema.Index{
				{Name: "idx_users_id", Columns: []string{"id"}},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "email", Type: "TEXT", Nullable: false},
			},
			Indexes: []schema.Index{
				{Name: "idx_users_id", Columns: []string{"id"}},
				{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
			},
		},
		{
			Name: "profiles",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
		},
	}}

	changes := Between(current, desired)
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3: %#v", len(changes), changes)
	}
	if changes[0].Kind != KindAddColumn || changes[0].Table != "users" || changes[0].Column.Name != "email" {
		t.Fatalf("unexpected first change: %#v", changes[0])
	}
	if changes[1].Kind != KindAddIndex || changes[1].Index.Name != "idx_users_email" {
		t.Fatalf("unexpected second change: %#v", changes[1])
	}
	if changes[2].Kind != KindAddTable || changes[2].Table != "profiles" {
		t.Fatalf("unexpected third change: %#v", changes[2])
	}
}

func TestBetweenTreatsNilCurrentAsEmptySchema(t *testing.T) {
	t.Parallel()

	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
		},
	}}

	changes := Between(nil, desired)
	if len(changes) != 1 {
		t.Fatalf("changes = %d, want 1: %#v", len(changes), changes)
	}
	if changes[0].Kind != KindAddTable || changes[0].Table != "users" {
		t.Fatalf("unexpected change: %#v", changes[0])
	}
}

func TestBetweenSkipsEquivalentIndexWithDifferentName(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "email", Type: "TEXT"},
			},
			Indexes: []schema.Index{
				{Name: "sqlite_autoindex_users_1", Columns: []string{"email"}, Unique: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "email", Type: "TEXT"},
			},
			Indexes: []schema.Index{
				{Name: "uni_users_email", Columns: []string{"email"}, Unique: true},
			},
		},
	}}

	if changes := Between(current, desired); len(changes) != 0 {
		t.Fatalf("changes = %#v, want none", changes)
	}
	if drifts := DetectDrifts(current, desired); len(drifts) != 0 {
		t.Fatalf("drifts = %#v, want none", drifts)
	}
}

func TestBetweenReportsNonAutoIndexWithDifferentName(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name:    "users",
			Columns: []schema.Column{{Name: "email", Type: "TEXT"}},
			Indexes: []schema.Index{
				{Name: "idx_users_email_old", Columns: []string{"email"}, Unique: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name:    "users",
			Columns: []schema.Column{{Name: "email", Type: "TEXT"}},
			Indexes: []schema.Index{
				{Name: "idx_users_email_new", Columns: []string{"email"}, Unique: true},
			},
		},
	}}

	if changes := Between(current, desired); len(changes) != 1 || changes[0].Kind != KindAddIndex {
		t.Fatalf("changes = %#v, want add index", changes)
	}
	if drifts := DetectDrifts(current, desired); len(drifts) != 1 || drifts[0].Name != "idx_users_email_old" {
		t.Fatalf("drifts = %#v, want current-only old index", drifts)
	}
}

func TestBetweenDoesNotCreateIndexWhenUnsupportedIndexNameExists(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name:    "users",
			Columns: []schema.Column{{Name: "email", Type: "TEXT"}},
			Indexes: []schema.Index{
				{Name: "idx_users_email", Unsupported: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name:    "users",
			Columns: []schema.Column{{Name: "email", Type: "TEXT"}},
			Indexes: []schema.Index{
				{Name: "idx_users_email", Columns: []string{"email"}},
			},
		},
	}}

	if changes := Between(current, desired); len(changes) != 0 {
		t.Fatalf("changes = %#v, want none", changes)
	}
	drifts := DetectDrifts(current, desired)
	if len(drifts) != 1 || drifts[0].Kind != DriftIndex || drifts[0].Name != "idx_users_email" {
		t.Fatalf("drifts = %#v, want unsupported index drift", drifts)
	}
}

func TestDetectDriftsReportsExistingStructureMismatch(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "email", Type: "TEXT", Nullable: true},
			},
			Indexes: []schema.Index{
				{Name: "idx_users_email", Columns: []string{"email"}, Unique: false},
				{Name: "idx_users_legacy", Columns: []string{"legacy"}},
			},
		},
		{
			Name: "legacy_users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "email", Type: "VARCHAR(128)", Nullable: false},
			},
			Indexes: []schema.Index{
				{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
			},
		},
	}}

	drifts := DetectDrifts(current, desired)
	if len(drifts) != 4 {
		t.Fatalf("drifts = %d, want 4: %#v", len(drifts), drifts)
	}
	if drifts[0].Kind != DriftIndex || drifts[0].Table != "users" || drifts[0].Name != "idx_users_legacy" {
		t.Fatalf("unexpected current-only index drift: %#v", drifts[0])
	}
	if drifts[1].Kind != DriftTable || drifts[1].Table != "legacy_users" {
		t.Fatalf("unexpected current-only table drift: %#v", drifts[1])
	}
	if drifts[2].Kind != DriftColumn || drifts[2].Table != "users" || drifts[2].Name != "email" {
		t.Fatalf("unexpected column drift: %#v", drifts[2])
	}
	if drifts[3].Kind != DriftIndex || drifts[3].Table != "users" || drifts[3].Name != "idx_users_email" {
		t.Fatalf("unexpected index drift: %#v", drifts[3])
	}
	if len(drifts.Messages()) != 4 {
		t.Fatalf("expected drift messages, got %#v", drifts.Messages())
	}
}

func TestDetectDriftsNormalizesEquivalentDefaultsAndAutoIncrement(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "BIGINT", PrimaryKey: true, AutoIncrement: true, DefaultValue: "nextval('users_id_seq'::regclass)"},
				{Name: "name", Type: "VARCHAR(128)", DefaultValue: "anonymous"},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "BIGINT", PrimaryKey: true, AutoIncrement: true},
				{Name: "name", Type: "VARCHAR(128)", DefaultValue: "'anonymous'"},
			},
		},
	}}

	if drifts := DetectDrifts(current, desired); len(drifts) != 0 {
		t.Fatalf("drifts = %#v, want none", drifts)
	}
}

func TestBetweenAndDetectDriftsUseQualifiedTableIdentity(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Schema: "public",
			Name:   "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "public.users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "email", Type: "TEXT"},
			},
		},
	}}

	changes := Between(current, desired)
	if len(changes) != 1 || changes[0].Kind != KindAddColumn || changes[0].Table != "public.users" {
		t.Fatalf("changes = %#v, want one add-column on public.users", changes)
	}
	if drifts := DetectDrifts(current, desired); len(drifts) != 0 {
		t.Fatalf("drifts = %#v, want none", drifts)
	}
}

func TestDetectDriftsTreatsNumericPrecisionZeroScaleAsEquivalent(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "orders",
			Columns: []schema.Column{
				{Name: "amount", Type: "NUMERIC(10)", Nullable: false},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "orders",
			Columns: []schema.Column{
				{Name: "amount", Type: "NUMERIC(10,0)", Nullable: false},
			},
		},
	}}

	if drifts := DetectDrifts(current, desired); len(drifts) != 0 {
		t.Fatalf("drifts = %#v, want none", drifts)
	}
}

func TestDetectDriftsUsesExplicitDetailForCurrentOnlyColumn(t *testing.T) {
	t.Parallel()

	current := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "legacy_code", Type: "TEXT"},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
			},
		},
	}}

	drifts := DetectDrifts(current, desired)
	if len(drifts) != 1 {
		t.Fatalf("drifts = %#v, want one current-only column drift", drifts)
	}
	if drifts[0].Kind != DriftColumn || drifts[0].Name != "legacy_code" {
		t.Fatalf("unexpected drift: %#v", drifts[0])
	}
	if drifts[0].Detail != "column exists in database but not in desired schema" {
		t.Fatalf("unexpected current-only column detail: %#v", drifts[0])
	}
}
