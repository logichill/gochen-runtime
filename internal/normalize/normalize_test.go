package normalize

import (
	"reflect"
	"testing"
)

func TestStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "nil slice",
			in:   nil,
			want: nil,
		},
		{
			name: "empty slice",
			in:   []string{},
			want: nil,
		},
		{
			name: "all empty or spaces",
			in:   []string{"", "  ", "\t\n"},
			want: nil,
		},
		{
			name: "deduplicate and trim whitespace preserving order",
			in:   []string{" a ", "b", "a", "c", "  b  ", "d"},
			want: []string{"a", "b", "c", "d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Strings(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Strings(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPositiveID(t *testing.T) {
	tests := []struct {
		in   int64
		want int64
	}{
		{in: -10, want: 0},
		{in: 0, want: 0},
		{in: 1, want: 1},
		{in: 9999, want: 9999},
	}

	for _, tt := range tests {
		got := PositiveID(tt.in)
		if got != tt.want {
			t.Errorf("PositiveID(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
