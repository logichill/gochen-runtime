package rest

import (
	"reflect"
	"strings"
	"testing"

	"gochen/app/query"
	"gochen/errors"
)

func TestParseSortsParam_InvalidDirectionKeepsSortContext(t *testing.T) {
	_, err := parseSortsParam([]string{"created_at:down"})
	if err == nil {
		t.Fatalf("expected invalid sort direction error")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput error, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid sorts syntax") {
		t.Fatalf("expected invalid sorts syntax wrapper, got %v", err)
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T", err)
	}
	details := appErr.Details()
	if details["sort"] != "created_at:down" {
		t.Fatalf("expected sort context, got %#v", details)
	}
	if details["direction"] != "down" {
		t.Fatalf("expected direction context, got %#v", details)
	}
}

func TestNonEmptyStrings_TrimAndDropBlanks(t *testing.T) {
	got := nonEmptyStrings([]string{"  a  ", "", "   ", "b"})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected strings: got=%v want=%v", got, want)
	}

	if got := nonEmptyStrings([]string{"", "   "}); got != nil {
		t.Fatalf("expected nil for blank-only input, got %v", got)
	}
}

func TestValidateSchemaSortsRejectsNonSortableField(t *testing.T) {
	schema := query.NewQuerySchema(
		query.QueryField{Name: "name", Type: query.FieldTypeString},
		query.QueryField{Name: "created_at", Type: query.FieldTypeTime, Sortable: true},
	)

	err := validateSchemaSorts([]query.Sort{{Field: "name", Direction: query.ASC}}, schema)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T", err)
	}
	if got := appErr.Details()["field"]; got != "name" {
		t.Fatalf("expected field context name, got %#v", got)
	}
}

type fakeEntity struct {
	ID int64
}

func (e *fakeEntity) GetID() int64       { return e.ID }
func (e *fakeEntity) GetVersion() uint64 { return 0 }

func TestValidateBulkEntitiesAddsIndexAndShortCircuits(t *testing.T) {
	calls := 0
	rb := &RouteBuilder[*fakeEntity, int64]{
		config: &RouteConfig[int64]{},
	}
	rb.config.Body.Validator = func(any) error {
		calls++
		if calls == 2 {
			return errors.NewCode(errors.InvalidInput, "bad entity")
		}
		return nil
	}

	err := rb.validateBulkEntities([]*fakeEntity{{ID: 1}, {ID: 2}, {ID: 3}})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected validator to short-circuit after 2 calls, got %d", calls)
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T", err)
	}
	if got := appErr.Details()["index"]; got != 1 {
		t.Fatalf("expected index 1, got %#v", got)
	}
}
