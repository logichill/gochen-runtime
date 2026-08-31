package schema

import "testing"

func TestNormalizeColumnType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "numeric precision expands zero scale", in: "NUMERIC(10)", want: "NUMERIC(10,0)"},
		{name: "numeric explicit zero scale kept", in: "numeric(10,0)", want: "NUMERIC(10,0)"},
		{name: "decimal explicit scale kept", in: "decimal(12,2)", want: "DECIMAL(12,2)"},
		{name: "non numeric untouched", in: "VARCHAR(128)", want: "VARCHAR(128)"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeColumnType(tt.in); got != tt.want {
				t.Fatalf("NormalizeColumnType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
