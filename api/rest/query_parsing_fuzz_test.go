package rest

import "testing"

func FuzzParseFilterParam(f *testing.F) {
	seeds := []string{
		"",
		"  ",
		"name:eq:alice",
		"age:gt:18",
		"deleted_at:is_null",
		"tags:in:a,b,c",
		"tags:not_in:a,b",
		"field:like:",
		"bad-syntax",
		"field:unknown:1",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// Keep fuzz inputs bounded to avoid accidental memory blowups.
		if len(raw) > 4096 {
			return
		}
		_, _ = parseFilterParam(raw)
	})
}

func FuzzParseSortsParam(f *testing.F) {
	seeds := []string{
		"",
		"  ",
		"id",
		"id:asc",
		"id:desc,created_at:desc",
		"  id : asc ,  created_at : desc ",
		",,,",
		":asc",
		"field:unknown",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		_, _ = parseSortsParam([]string{raw})
	})
}
