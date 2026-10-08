package repo

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen/db/orm"
)

func TestListByIDsPredicateRespectsBindBudget(t *testing.T) {
	type byteValues []byte
	for _, tc := range []struct {
		name       string
		args       []any
		expr       string
		statements int
	}{
		{"bytes", []any{[]byte{1, 2, 3}}, "id = ?", 1},
		{"nil_bytes", []any{[]byte(nil)}, "id = ?", 1},
		{"byte_array", []any{[3]byte{1, 2, 3}}, "id IN ?", 2},
		{"named_bytes", []any{byteValues{1, 2, 3}}, "id IN ?", 2},
		{"integers", []any{[]int{1, 2, 3}}, "id IN ?", 2},
		{"nil", []any{nil}, "id = ?", 1},
		{"empty_slice", []any{[]int{}, 1}, "id IN ? AND id <> ?", 2},
		{"nil_slice", []any{[]int(nil)}, "id IN ?", 1},
		{"empty_array", []any{[0]int{}}, "id IN ?", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, capture := newCaptureRepo(t)
			engine, err := lite.New(smallBudgetDB{capture})
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRepo[*plainCaptureEntity, int64](engine, "capture_entities", WithReadPredicate[*plainCaptureEntity, int64](func(context.Context) (orm.Condition, error) {
				return orm.Condition{Expr: tc.expr, Args: tc.args}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 99)
			for i := range ids {
				ids[i] = int64(i + 1)
			}
			capture.reset()
			if _, err := r.ListByIds(t.Context(), ids); err != nil {
				t.Fatal(err)
			}
			statements := capture.captured()
			if len(statements) != tc.statements {
				t.Fatalf("got %d statements, want %d", len(statements), tc.statements)
			}
			for _, stmt := range statements {
				if len(stmt.Args) > 100 {
					t.Errorf("emitted %d bind parameters with configured maximum of 100", len(stmt.Args))
				}
			}
		})
	}
}
