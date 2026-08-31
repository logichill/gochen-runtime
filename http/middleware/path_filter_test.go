package middleware

import "testing"

func TestPathFilterNormalizesAndMatchesPaths(t *testing.T) {
	filter := newPathFilter(
		[]string{" /health ", "", "   "},
		[]string{" /internal/ ", ""},
	)

	tests := []struct {
		path string
		want bool
	}{
		{path: "/health", want: true},
		{path: " /health ", want: false},
		{path: "/internal/status", want: true},
		{path: "/api/health", want: false},
	}
	for _, test := range tests {
		if got := filter.matches(test.path); got != test.want {
			t.Fatalf("matches(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}
