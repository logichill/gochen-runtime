package repo

import (
	"reflect"
	"strings"
	"testing"

	runtimedialect "gochen-runtime/db/dialect"
	"gochen/app"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/errors"
)

func TestProtectedStringSQLDialects(t *testing.T) {
	type document struct {
		crud.Entity[int64]
		Region string `dim:"region"`
	}
	metadata, err := meta.Describe[*document]()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := app.BindContract(metadata, app.ResourceContract{})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ name, expression string }{
		{"sqlite", `CAST("region" AS BLOB) = CAST(? AS BLOB)`},
		{"mysql", "CAST(`region` AS BINARY) = CAST(? AS BINARY)"},
		{"postgres", `convert_to(CAST("region" AS TEXT), 'UTF8') = convert_to(CAST(? AS TEXT), 'UTF8')`},
		{"unknown", ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			resolver, err := newColumnResolver(metadata, nil, runtimedialect.New(fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			repository := &Repo[*document, int64]{resolver: resolver, execution: contract}
			condition, err := repository.protectedCondition(map[string]any{"Region": "north "})
			if fixture.expression == "" {
				if !errors.Is(err, errors.Unsupported) {
					t.Fatalf("unknown dialect: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(condition.Expr, fixture.expression) || !reflect.DeepEqual(condition.Args, []any{"north "}) {
				t.Fatalf("condition=%+v error=%v", condition, err)
			}
		})
	}
}

func TestAuditIdentityConversionKeepsBusinessType(t *testing.T) {
	type row struct {
		ID string `entity:"id"`
	}
	metadata, err := meta.Describe[*row]()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name     string
		identity reflect.Type
		input    any
		want     string
		invalid  bool
	}{
		{"signed", reflect.TypeFor[int64](), "0042", "42", false},
		{"unsigned", reflect.TypeFor[uint64](), "18446744073709551615", "18446744073709551615", false},
		{"small_overflow", reflect.TypeFor[int8](), "128", "", true},
		{"unsigned_negative", reflect.TypeFor[uint64](), "-1", "", true},
		{"string", reflect.TypeFor[string](), "0042", "0042", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			resolver, err := newColumnResolver(metadata, nil, runtimedialect.New("sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			resolver.identityType = fixture.identity
			value, err := convertSpecValue(meta.ID(), fixture.input, resolver)
			if fixture.invalid {
				if !errors.Is(err, errors.Internal) {
					t.Fatalf("invalid ID conversion: %v", err)
				}
			} else if err != nil || value != fixture.want {
				t.Fatalf("converted=%v error=%v, want=%q", value, err, fixture.want)
			}
		})
	}
}
