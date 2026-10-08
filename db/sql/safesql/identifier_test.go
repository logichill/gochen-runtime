package safesql

import (
	"testing"

	"gochen-runtime/db/dialect"
)

func TestQuoteQualifiedIdentifier(t *testing.T) {
	got, err := QuoteQualifiedIdentifier(dialect.New("postgres"), " public.users ", "", "table name", 2)
	if err != nil {
		t.Fatalf("QuoteQualifiedIdentifier returned error: %v", err)
	}
	if got != `"public"."users"` {
		t.Fatalf("QuoteQualifiedIdentifier = %s", got)
	}

	if _, err := QuoteQualifiedIdentifier(dialect.New("postgres"), "public.users.extra", "", "table name", 2); err == nil {
		t.Fatal("expected too many segments to fail")
	}
}
