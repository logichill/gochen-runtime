package migrate

import (
	"reflect"
	"testing"
)

func TestSplitStatementsHandlesQuotesAndComments(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
-- comment with ; should stay attached
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT DEFAULT ';');
INSERT INTO users (name) VALUES ('a; b');
INSERT INTO users (name) VALUES ('it''s; ok');
/* block ; comment */
CREATE INDEX idx_users_name ON users("name");
`)
	want := []string{
		"-- comment with ; should stay attached\nCREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT DEFAULT ';')",
		"INSERT INTO users (name) VALUES ('a; b')",
		"INSERT INTO users (name) VALUES ('it''s; ok')",
		"/* block ; comment */\nCREATE INDEX idx_users_name ON users(\"name\")",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesMySQLHashComments(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
# comment with ; should stay attached
CREATE TABLE users (id INTEGER PRIMARY KEY);
INSERT INTO users (id) VALUES (1);
`)
	want := []string{
		"# comment with ; should stay attached\nCREATE TABLE users (id INTEGER PRIMARY KEY)",
		"INSERT INTO users (id) VALUES (1)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesPostgresDollarQuotes(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
CREATE FUNCTION touch_updated_at()
RETURNS trigger AS $$
BEGIN
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER users_touch BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE FUNCTION tagged()
RETURNS text AS $body$
BEGIN
  RETURN 'a;b';
END;
$body$ LANGUAGE plpgsql;
`)

	want := []string{
		`CREATE FUNCTION touch_updated_at()
RETURNS trigger AS $$
BEGIN
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`,
		`CREATE TRIGGER users_touch BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION touch_updated_at()`,
		`CREATE FUNCTION tagged()
RETURNS text AS $body$
BEGIN
  RETURN 'a;b';
END;
$body$ LANGUAGE plpgsql`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesBackslashEscapedQuotes(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
INSERT INTO notes (body) VALUES (E'it\'s; ok');
INSERT INTO notes (body) VALUES ('it''s; ok');
SELECT 1;
`)
	want := []string{
		`INSERT INTO notes (body) VALUES (E'it\'s; ok')`,
		`INSERT INTO notes (body) VALUES ('it''s; ok')`,
		`SELECT 1`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsDoesNotTreatBackslashAsGenericSingleQuoteEscape(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
INSERT INTO notes (body) VALUES ('C:\path\');
SELECT 1;
`)
	want := []string{
		`INSERT INTO notes (body) VALUES ('C:\path\')`,
		`SELECT 1`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesDoubleQuoteAndBacktickEscape(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
ALTER TABLE t RENAME COLUMN "old""name" TO "new""name";
ALTER TABLE t RENAME COLUMN ` + "`old``name`" + ` TO ` + "`new``name`" + `;
SELECT 1;
`)
	want := []string{
		`ALTER TABLE t RENAME COLUMN "old""name" TO "new""name"`,
		"ALTER TABLE t RENAME COLUMN `old``name` TO `new``name`",
		`SELECT 1`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesNestedBlockComments(t *testing.T) {
	t.Parallel()

	got := SplitStatements(`
/* outer /* inner ; comment */ still ; comment */
SELECT 1;
SELECT 2;
`)
	want := []string{
		`/* outer /* inner ; comment */ still ; comment */
SELECT 1`,
		`SELECT 2`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsHandlesPostgresPrefixedStrings(t *testing.T) {
	t.Parallel()

	got := SplitStatements(
		"INSERT INTO blobs (value) VALUES (B'10;01');\n" +
			"INSERT INTO blobs (value) VALUES (X'CA;FE');\n" +
			"SELECT U&'a;b';\n" +
			"SELECT 1;",
	)
	want := []string{
		"INSERT INTO blobs (value) VALUES (B'10;01')",
		"INSERT INTO blobs (value) VALUES (X'CA;FE')",
		"SELECT U&'a;b'",
		"SELECT 1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements() = %#v, want %#v", got, want)
	}
}
