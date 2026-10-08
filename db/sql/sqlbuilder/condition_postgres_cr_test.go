package sqlbuilder

import (
	"strings"
	"testing"

	"gochen-runtime/db/dialect"
	"gochen/errors"
)

func TestPostgresConditionsRespectCarriageReturn(t *testing.T) {
	builder, err := New(&fakeDB{dialectName: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"select", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			condition := "id > 0 -- comment\r OR id = 2"
			var query string
			switch operation {
			case "select":
				query, _, err = builder.Select("id").From("items").Where(condition).Where("tenant_id = 1").Build()
			case "update":
				query, _, err = builder.Update("items").Set("value", 1).Where(condition).Where("tenant_id = 1").Build()
			case "delete":
				query, _, err = builder.DeleteFrom("items").Where(condition).Where("tenant_id = 1").Build()
			}
			if err != nil || !strings.Contains(query, "WHERE ("+condition+"\n) AND tenant_id = 1") {
				t.Fatalf("OR after CR escaped grouping: query=%q err=%v", query, err)
			}
		})
	}
	t.Run("binding", func(t *testing.T) {
		query, args, err := builder.Select("id").From("items").Where("id > 0 -- comment\r OR payload ? 'key' OR id = ?", 2).Where("tenant_id = ?", 1).Build()
		bound := dialect.New("postgres").Rebind(query)
		if err != nil || len(args) != 2 || !strings.Contains(bound, "payload ? 'key'") || !strings.Contains(bound, "id = $1") || !strings.Contains(bound, "tenant_id = $2") {
			t.Fatalf("placeholder after CR hidden: query=%q args=%v err=%v", bound, args, err)
		}
	})
	t.Run("separator", func(t *testing.T) {
		_, _, err := builder.Select("id").From("items").Where("id > 0 -- comment\r; SELECT 1").Where("tenant_id = 1").Build()
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("statement separator after CR accepted: %v", err)
		}
	})
}
