package httpheader

import (
	"net/http"
	"testing"
)

func TestBuildAllowHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		methods map[string]struct{}
		want    string
	}{
		{
			name: "empty input",
			want: http.MethodOptions,
		},
		{
			name: "single method",
			methods: map[string]struct{}{
				http.MethodGet: {},
			},
			want: "GET, OPTIONS",
		},
		{
			name: "includes options once",
			methods: map[string]struct{}{
				http.MethodPost:    {},
				http.MethodOptions: {},
			},
			want: "POST, OPTIONS",
		},
		{
			name: "non standard methods sorted after known methods",
			methods: map[string]struct{}{
				http.MethodDelete: {},
				"BREW":            {},
				"ACL":             {},
			},
			want: "DELETE, OPTIONS, ACL, BREW",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildAllowHeader(tt.methods); got != tt.want {
				t.Fatalf("BuildAllowHeader() = %q, want %q", got, tt.want)
			}
		})
	}
}
