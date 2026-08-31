package safeident

import "testing"

func TestIsSafeIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "simple", in: "users", want: true},
		{name: "underscore", in: "user_profiles", want: true},
		{name: "qualified", in: "public.users", want: true},
		{name: "starts_with_digit", in: "1users", want: false},
		{name: "contains_dash", in: "user-profiles", want: false},
		{name: "contains_space", in: "user profiles", want: false},
		{name: "empty", in: "", want: false},
		{name: "empty_segment", in: "public..users", want: false},
		{name: "trailing_dot", in: "public.", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSafeIdentifier(tt.in); got != tt.want {
				t.Fatalf("IsSafeIdentifier(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateQualifiedIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxParts int
		wantErr  bool
	}{
		{name: "simple", in: "users", maxParts: 2},
		{name: "qualified", in: "public.users", maxParts: 2},
		{name: "too_many_parts", in: "a.b.c", maxParts: 2, wantErr: true},
		{name: "empty_segment", in: "public.", maxParts: 2, wantErr: true},
		{name: "unsafe_segment", in: "public.user-profiles", maxParts: 2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateQualifiedIdentifier(tt.in, "table name", tt.maxParts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateQualifiedIdentifier(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
		})
	}
}
