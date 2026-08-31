package rest

import (
	"encoding/json"
	"testing"

	"gochen/errors"
)

func TestRejectAuditedUpdateForbiddenFields_IsCaseInsensitive(t *testing.T) {
	for _, field := range []string{
		"CREATED_AT",
		"Created_By",
		"UPDATED_AT",
		"Updated_By",
		"DELETED_AT",
		"Deleted_By",
	} {
		t.Run(field, func(t *testing.T) {
			body := map[string]json.RawMessage{field: json.RawMessage(`null`)}
			if err := rejectAuditedUpdateForbiddenFields(body); !errors.Is(err, errors.InvalidInput) {
				t.Fatalf("expected InvalidInput for %q, got %v", field, err)
			}
		})
	}
}

func TestRequireUint64JSONField_RejectsFoldedDuplicate(t *testing.T) {
	body := map[string]json.RawMessage{
		"version": json.RawMessage(`7`),
		"VERSION": json.RawMessage(`8`),
	}
	if _, err := requireUint64JSONField(body, "version"); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for ambiguous version, got %v", err)
	}
}
