package request

import (
	"net/url"
	"testing"
	"time"

	"gochen/errors"
)

func TestBindQuery_BindsAliasesNestedAndSlices(t *testing.T) {
	type filter struct {
		Active bool `query:"active"`
	}
	type query struct {
		Name    string        `query:"name"`
		Page    int           `query:"page"`
		Timeout time.Duration `query:"timeout"`
		Tags    []string      `query:"tag"`
		Filter  filter        `query:"filter"`
	}

	var got query
	err := BindQuery(&got, url.Values{
		"name":           {"alice"},
		"page":           {"2"},
		"timeout":        {"3s"},
		"tag":            {"go", "test"},
		"filter[active]": {"true"},
	})
	if err != nil {
		t.Fatalf("BindQuery error = %v", err)
	}
	if got.Name != "alice" || got.Page != 2 || got.Timeout != 3*time.Second || !got.Filter.Active {
		t.Fatalf("BindQuery scalar result = %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "go" || got.Tags[1] != "test" {
		t.Fatalf("BindQuery tags = %#v", got.Tags)
	}
}

func TestBindQuery_RejectsInvalidTargetAndValue(t *testing.T) {
	if err := BindQuery(nil, nil); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("BindQuery nil target error = %v, want InvalidInput", err)
	}

	var got struct {
		Page int `query:"page"`
	}
	err := BindQuery(&got, url.Values{"page": {"not-int"}})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("BindQuery invalid value error = %v, want InvalidInput", err)
	}
}

func TestBindQuery_RejectsNonFiniteFloat(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "+Inf", "-Inf"} {
		var got struct {
			Ratio float64 `query:"ratio"`
		}

		err := BindQuery(&got, url.Values{"ratio": {raw}})
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("BindQuery non-finite float %q error = %v, want InvalidInput", raw, err)
		}
	}
}
