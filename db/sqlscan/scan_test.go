package sqlscan

import "testing"

func TestScanNestedBlockComment(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		start int
		want  int
	}{
		{name: "simple", text: "/* comment */ SELECT 1", start: 0, want: len("/* comment */")},
		{name: "nested", text: "/* outer /* inner */ done */ SELECT 1", start: 0, want: len("/* outer /* inner */ done */")},
		{name: "unterminated", text: "SELECT /* open", start: len("SELECT "), want: len("SELECT /* open")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScanNestedBlockComment(tt.text, tt.start); got != tt.want {
				t.Fatalf("ScanNestedBlockComment() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScanSQLQuotedSpans(t *testing.T) {
	tests := []struct {
		name string
		text string
		scan func(string) int
		want int
	}{
		{
			name: "single quoted escaped quote",
			text: "'it''s ?' AND id = ?",
			scan: func(text string) int { return ScanSingleQuoted(text, 0) },
			want: len("'it''s ?'"),
		},
		{
			name: "backslash quoted",
			text: `E'it\'s ?' AND id = ?`,
			scan: func(text string) int { return ScanBackslashSingleQuoted(text, 1) },
			want: len(`E'it\'s ?'`),
		},
		{
			name: `quoted identifier`,
			text: `"weird""name" = ?`,
			scan: func(text string) int { return ScanQuoted(text, 0, '"') },
			want: len(`"weird""name"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scan(tt.text); got != tt.want {
				t.Fatalf("scan() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScanDollarQuoted(t *testing.T) {
	text := "$tag$ ? inside $tag$ AND id = ?"
	got, ok := ScanDollarQuoted(text, 0)
	if !ok {
		t.Fatal("expected dollar quote to be recognized")
	}
	if want := len("$tag$ ? inside $tag$"); got != want {
		t.Fatalf("ScanDollarQuoted() = %d, want %d", got, want)
	}
}

func TestScanPostgresPlaceholderIgnored(t *testing.T) {
	tests := []struct {
		name  string
		query string
		start int
		want  int
		ok    bool
	}{
		{name: "prefixed string", query: `E'?' AND id = ?`, start: 0, want: len(`E'?'`), ok: true},
		{name: "line comment", query: "-- ?\nSELECT ?", start: 0, want: len("-- ?\n"), ok: true},
		{name: "plain placeholder", query: "id = ?", start: len("id = "), want: len("id = "), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ScanPostgresPlaceholderIgnored(tt.query, tt.start)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ScanPostgresPlaceholderIgnored() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestIsPostgresJSONQuestionOperator(t *testing.T) {
	query := "payload ? 'name' AND id = ?"
	scanner := NewPrevTokenScanner(query)
	if !IsPostgresJSONQuestionOperator(query, &scanner, len("payload ")) {
		t.Fatal("expected first question mark to be JSON operator")
	}

	placeholder := len("payload ? 'name' AND id = ")
	if IsPostgresJSONQuestionOperator(query, &scanner, placeholder) {
		t.Fatal("expected bind placeholder not to be JSON operator")
	}
}
