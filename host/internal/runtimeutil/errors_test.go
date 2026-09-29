package runtimeutil

import (
	stderrors "errors"
	"testing"

	"gochen/errors"
)

func TestWrapModuleErrPreservesJoinedErrorSiblings(t *testing.T) {
	first := stderrors.New("first failure")
	second := stderrors.New("second failure")
	root := errors.NewCodeWithCause(errors.Conflict, "route failed", first).
		WithContext("reason", "test")

	wrapped := WrapModuleErr("module-a", stderrors.Join(root, second), "register routes", errors.Internal)
	if !stderrors.Is(wrapped, first) || !stderrors.Is(wrapped, second) {
		t.Fatalf("wrapped error lost a joined sibling: %v", wrapped)
	}
	var appErr *errors.AppError
	if !errors.As(wrapped, &appErr) || appErr == nil {
		t.Fatalf("wrapped error does not expose AppError: %v", wrapped)
	}
	if appErr.Code() != errors.Conflict {
		t.Fatalf("wrapped code = %q, want %q", appErr.Code(), errors.Conflict)
	}
	details := appErr.Details()
	if details["reason"] != "test" || details["module"] != "module-a" {
		t.Fatalf("wrapped details = %#v, want original and module context", details)
	}
}

func TestWrapModuleErrFallbackCodeForPlainError(t *testing.T) {
	wrapped := WrapModuleErr("module-b", stderrors.New("boom"), "start module", errors.Dependency)
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped error")
	}
	if wrapped.Code() != errors.Dependency {
		t.Fatalf("wrapped code = %q, want %q", wrapped.Code(), errors.Dependency)
	}
	if got := wrapped.Details()["module"]; got != "module-b" {
		t.Fatalf("module context = %#v, want module-b", got)
	}
	if WrapModuleErr("module-b", nil, "start module", errors.Dependency) != nil {
		t.Fatal("nil error should stay nil")
	}
}
