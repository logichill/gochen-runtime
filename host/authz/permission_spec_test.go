package authz

import (
	"reflect"
	"testing"
)

func TestPermissionSet_CodeAndMustExposeActionIndexedAccess(t *testing.T) {
	set := NewAPIPermissionSet("user", JoinActions(ManageActions(), SelfActions())...)

	if got := set.Code(PermissionActionRead); got != "user:api:read" {
		t.Fatalf("unexpected read code: %q", got)
	}
	if got := set.Must(PermissionActionSelfEdit).Code; got != "user:api:update_self" {
		t.Fatalf("unexpected self edit code: %q", got)
	}
}

func TestPermissionSet_MustAndCodeReturnZeroForMissingAction(t *testing.T) {
	set := NewAPIPermissionSet("user", PermissionActionRead)

	if got := set.Must(PermissionActionWrite); !reflect.DeepEqual(got, PermissionSpec{}) {
		t.Fatalf("expected zero spec, got %#v", got)
	}
	if got := set.Code(PermissionActionWrite); got != "" {
		t.Fatalf("expected empty code, got %q", got)
	}
}

func TestPermissionDefinitions_PreserveStructuredMetadata(t *testing.T) {
	definitions := PermissionDefinitions(
		APIPermission("llm", PermissionActionWrite).
			Label("LLM Write").
			Desc("Manage llm configuration.").
			Scope(PermissionScopePlatform, PermissionScopeTenant).
			Risk(PermissionRiskHigh),
	)
	if len(definitions) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(definitions))
	}
	got := definitions[0]
	if got.Code != "llm:api:write" || got.Type != "api" || got.Resource != "llm" || got.Action != "write" {
		t.Fatalf("unexpected definition identity: %#v", got)
	}
	if got.Name != "LLM Write" || got.Description != "Manage llm configuration." {
		t.Fatalf("unexpected definition metadata: %#v", got)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "platform" || got.Scopes[1] != "tenant" {
		t.Fatalf("unexpected definition scopes: %#v", got)
	}
	if got.RiskLevel != "high" {
		t.Fatalf("unexpected definition risk: %#v", got)
	}
}

func TestPermissionCode_ParsesNormalizedTriplet(t *testing.T) {
	spec := PermissionCode("LLM:API:READ")
	if spec.Code != "llm:api:read" || spec.Type != PermissionTypeAPI || spec.Resource != "llm" || spec.Action != PermissionActionRead {
		t.Fatalf("unexpected parsed spec: %#v", spec)
	}
}
