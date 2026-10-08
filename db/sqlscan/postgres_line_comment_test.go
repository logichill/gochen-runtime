package sqlscan

import "testing"

func TestPostgresLineCommentsEndAtCarriageReturn(t *testing.T) {
	// PostgreSQL scan.l: comment ("--"[^\n\r]*).
	for _, newline := range []string{"\r", "\n", "\r\n"} {
		prefix := "-- comment" + newline[:1]
		query := "-- comment" + newline + "id = ?"
		next, ok := ScanPostgresPlaceholderIgnored(query, 0)
		if !ok || next != len(prefix) {
			t.Errorf("comment %q: next=%d ok=%v want=%d", newline, next, ok, len(prefix))
		}
		start, end, ok := NextSQLToken(query, 0)
		if !ok || query[start:end] != "id" {
			t.Errorf("next token after %q: start=%d end=%d ok=%v", newline, start, end, ok)
		}
	}
	// 通用扫描器仍保留 SQLite/迁移调用者采用的 LF 终止语义。
	query := "-- comment\r id = ?"
	if next := ScanLineComment(query, 0); next != len(query) {
		t.Fatalf("generic line-comment semantics changed: next=%d", next)
	}
}
