package schema

import "testing"

func TestNormalizeDefaultValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		columnType string
		value      string
		want       string
	}{
		{
			name:       "mysql bare string default becomes quoted literal",
			columnType: "VARCHAR(128)",
			value:      "anonymous",
			want:       "'anonymous'",
		},
		{
			name:       "postgres string cast is stripped",
			columnType: "TEXT",
			value:      "'anonymous'::text",
			want:       "'anonymous'",
		},
		{
			name:       "postgres numeric cast is stripped",
			columnType: "BIGINT",
			value:      "42::bigint",
			want:       "42",
		},
		{
			name:       "nextval expression is preserved",
			columnType: "BIGINT",
			value:      "nextval('users_id_seq'::regclass)",
			want:       "nextval('users_id_seq'::regclass)",
		},
		{
			name:       "text column sql keyword default stays expression",
			columnType: "TEXT",
			value:      "CURRENT_TIMESTAMP",
			want:       "CURRENT_TIMESTAMP",
		},
		{
			name:       "quoted sql keyword stays string literal",
			columnType: "TEXT",
			value:      "'CURRENT_TIMESTAMP'",
			want:       "'CURRENT_TIMESTAMP'",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeDefaultValue(tt.columnType, tt.value, ""); got != tt.want {
				t.Fatalf("NormalizeDefaultValue(%q, %q) = %q, want %q", tt.columnType, tt.value, got, tt.want)
			}
		})
	}
}
