package di_test

import (
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
)

func TestDiagnose_Helper(t *testing.T) {
	c := basic.New()
	diag, ok := di.Diagnose(c)
	if !ok {
		t.Fatal("expected basic container to expose diagnostics")
	}
	if diag == nil || diag.RegisteredServices == nil {
		t.Fatalf("unexpected diagnose result: %#v", diag)
	}

	var nilContainer *basic.Container
	if diag, ok := di.Diagnose(nilContainer); ok || diag != nil {
		t.Fatalf("expected typed nil to be ignored, got ok=%v diag=%#v", ok, diag)
	}
	if diag, ok := di.Diagnose(struct{}{}); ok || diag != nil {
		t.Fatalf("expected non-diagnoser to be ignored, got ok=%v diag=%#v", ok, diag)
	}
}
