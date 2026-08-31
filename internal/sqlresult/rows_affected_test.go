package sqlresult

import (
	"database/sql/driver"
	"testing"

	"gochen/errors"
)

type unsupportedRowsAffectedResult struct{}

func (unsupportedRowsAffectedResult) RowsAffected() (int64, error) {
	return 0, driver.ErrSkip
}

func TestEnsureRowsAffected_ReturnsErrorForNilResult(t *testing.T) {
	err := EnsureRowsAffected(nil, 1, "unexpected rows")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, errors.Internal) {
		t.Fatalf("expected internal error, got %v", err)
	}
}

func TestEnsureRowsAffected_ReturnsUnsupportedWhenRowsAffectedIsUnavailable(t *testing.T) {
	err := EnsureRowsAffected(unsupportedRowsAffectedResult{}, 1, "unexpected rows")
	if err == nil {
		t.Fatalf("expected unsupported error")
	}
	if !errors.Is(err, errors.Unsupported) {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestEnsureRowsAffected_ReturnsConflictOnMismatch(t *testing.T) {
	err := EnsureRowsAffected(driver.RowsAffected(0), 1, "unexpected rows")
	if err == nil {
		t.Fatalf("expected conflict")
	}
	if !errors.Is(err, errors.Conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}
