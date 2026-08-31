package migrate

import "testing"

func TestContainsManualReviewGuardMatchesStatementOnly(t *testing.T) {
	t.Parallel()

	if !containsManualReviewGuard("-- review required\n" + ManualReviewGuardStatement + ";\nDROP TABLE users;") {
		t.Fatal("expected exact guard statement to be detected")
	}
	if !containsManualReviewGuard("# review required\n" + ManualReviewGuardStatement + ";\nDROP TABLE users;") {
		t.Fatal("expected guard after hash comment to be detected")
	}
	if containsManualReviewGuard(`
-- mention gochen_migration_down_requires_manual_review in comment only
DROP TABLE users;
`) {
		t.Fatal("expected comment-only mention not to trigger guard")
	}
	if containsManualReviewGuard(`
INSERT INTO notes(body) VALUES ('gochen_migration_down_requires_manual_review');
`) {
		t.Fatal("expected string literal mention not to trigger guard")
	}
}

func TestContainsManualReviewGuardIgnoresDollarQuotedAndPrefixedStrings(t *testing.T) {
	t.Parallel()

	dollar := "$guard$SELECT " + ManualReviewGuardFunction + "()$guard$"
	if containsManualReviewGuard(dollar + ";") {
		t.Fatal("expected dollar-quoted guard mention not to trigger guard")
	}
	if containsManualReviewGuard("B'SELECT " + ManualReviewGuardFunction + "()';") {
		t.Fatal("expected prefixed string guard mention not to trigger guard")
	}
}

func TestContainsManualReviewGuardIgnoresNestedCommentsAndMatchesCompactedStatement(t *testing.T) {
	t.Parallel()

	if !containsManualReviewGuard(
		"/* outer /* nested */ comment */\n" +
			" SeLeCt /* inline */ " + ManualReviewGuardFunction + " ( ) ;",
	) {
		t.Fatal("expected comments and whitespace around the guard to be ignored")
	}
}
