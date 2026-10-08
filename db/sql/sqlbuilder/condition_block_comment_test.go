package sqlbuilder

import (
	"database/sql"
	"strings"
	"testing"

	"gochen/errors"
)

func TestConditionsRejectUnterminatedTokens(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		expressions := []string{"id > 0 /* unfinished", "name = 'unfinished", "name = 'escaped''", `name = "unfinished`}
		switch dialect {
		case "sqlite":
			expressions = append(expressions, "[unfinished", "`unfinished")
		case "mysql":
			expressions = append(expressions, "`unfinished", `name = 'escaped\'`)
		case "postgres":
			expressions = append(expressions, "name = $$unfinished", "name = $tag$unfinished", `name = E'escaped\'`, "id > 0 /* outer /* inner */")
		}
		for _, expr := range expressions {
			for _, operation := range []string{"select", "or", "update", "delete"} {
				t.Run(dialect+"/"+operation+"/"+expr, func(t *testing.T) {
					builder, err := New(&fakeDB{dialectName: dialect})
					if err != nil {
						t.Fatal(err)
					}
					var query string
					switch operation {
					case "select":
						query, _, err = builder.Select("id").From("items").Where(expr).Where("tenant_id = 1").Build()
					case "or":
						query, _, err = builder.Select("id").From("items").Where("id = 0").Or(expr).Where("tenant_id = 1").Build()
					case "update":
						query, _, err = builder.Update("items").Set("value", 1).Where(expr).Where("tenant_id = 1").Build()
					case "delete":
						query, _, err = builder.DeleteFrom("items").Where(expr).Where("tenant_id = 1").Build()
					}
					if !errors.Is(err, errors.InvalidInput) {
						if dialect == "sqlite" && operation == "select" && expr == "id > 0 /* unfinished" {
							database := openCommentTestDB(t)
							rows, queryErr := database.Query(query)
							if queryErr != nil {
								t.Fatal(queryErr)
							}
							defer rows.Close()
							count := 0
							for rows.Next() {
								count++
							}
							t.Fatalf("unterminated comment returned %d tenants' rows (rows error %v): %s", count, rows.Err(), query)
						}
						t.Fatalf("expected InvalidInput, got %v for %s", err, query)
					}
				})
			}
		}
	}
}

func TestBlockCommentsFollowDialectNesting(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		for _, operation := range []string{"select", "update", "delete"} {
			t.Run(dialect+"/"+operation, func(t *testing.T) {
				builder, err := New(&fakeDB{dialectName: dialect})
				if err != nil {
					t.Fatal(err)
				}
				condition := "id > 0 /* outer /* inner */ OR 1=1 /* outer-end */"
				var query string
				var args []any
				switch operation {
				case "select":
					query, args, err = builder.Select("id").From("items").Where(condition).Where("tenant_id = 1").Build()
				case "update":
					query, args, err = builder.Update("items").Set("value", 1).Where(condition).Where("tenant_id = 1").Build()
				case "delete":
					query, args, err = builder.DeleteFrom("items").Where(condition).Where("tenant_id = 1").Build()
				}
				if err != nil {
					t.Fatal(err)
				}
				database := openCommentTestDB(t)
				var count int64
				if operation == "select" {
					rows, err := database.Query(query, args...)
					if err != nil {
						t.Fatal(err)
					}
					defer rows.Close()
					for rows.Next() {
						count++
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
				} else {
					result, err := database.Exec(query, args...)
					if err != nil {
						t.Fatal(err)
					}
					count, err = result.RowsAffected()
					if err != nil {
						t.Fatal(err)
					}
				}
				if count != 1 {
					t.Fatalf("statement read/affected %d tenants' rows: %s", count, query)
				}
			})
		}
	}
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(dialect+"/placeholders", func(t *testing.T) {
			builder, err := New(&fakeDB{dialectName: dialect})
			if err != nil {
				t.Fatal(err)
			}
			args := []any{1, 2}
			condition := "id = ? /* outer /* inner */ OR id = ? /* outer-end */"
			if dialect == "postgres" {
				args = args[:1]
				condition = "id = ? /* outer /* inner */ OR id = ? outer-end */"
			}
			query, flat, err := builder.Select("id").From("items").Where(condition, args...).Where("tenant_id = 1").Build()
			if err != nil || len(flat) != len(args) {
				t.Fatalf("incorrect dialect comment boundary: query=%s args=%v error=%v", query, flat, err)
			}
		})
	}
}

func TestConditionsAcceptClosedTokensAtEOF(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		expressions := []string{"id > 0 /* complete */", "name = 'escaped'''", `name = "complete"`, "id > 0 -- comment"}
		if dialect == "postgres" {
			expressions = append(expressions, "name = $$complete$$", "name = $tag$complete$tag$", `name = E'escaped\''`, "id > 0 /* outer /* inner */ end */")
		}
		if dialect == "sqlite" {
			expressions = append(expressions, "[complete] = [complete]", "`complete` = `complete`")
		}
		if dialect == "mysql" {
			expressions = append(expressions, "id > 0 # comment", `name = 'escaped\''`)
		}
		for _, expr := range expressions {
			t.Run(dialect+"/"+expr, func(t *testing.T) {
				builder, err := New(&fakeDB{dialectName: dialect})
				if err != nil {
					t.Fatal(err)
				}
				query, _, err := builder.Select("id").From("items").Where(expr).Where("tenant_id = 1").Build()
				if err != nil || !strings.Contains(query, "tenant_id = 1") {
					t.Fatalf("closed token rejected: %s: %v", query, err)
				}
			})
		}
	}
}

func openCommentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec("CREATE TABLE items (id INTEGER, tenant_id INTEGER, value INTEGER); INSERT INTO items VALUES (1,1,0),(2,2,0)"); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestMySQLConditionsRejectExecutableComments(t *testing.T) {
	builder, err := New(&fakeDB{dialectName: "mysql"})
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"id > 0 /*! OR 1=1 */", "id > 0 /*!80000 OR 1=1 */", "id > 0 /*M! OR 1=1 */"} {
		builds := []func() (string, []any, error){
			builder.Select("id").From("items").Where(expr).Where("tenant_id = 1").Build,
			builder.Select("id").From("items").Where("id = 0").Or(expr).Where("tenant_id = 1").Build,
			builder.Update("items").Set("value", 1).Where(expr).Where("tenant_id = 1").Build,
			builder.DeleteFrom("items").Where(expr).Where("tenant_id = 1").Build,
		}
		for _, build := range builds {
			if query, _, err := build(); !errors.Is(err, errors.InvalidInput) {
				t.Fatalf("executable comment accepted: query=%s error=%v", query, err)
			}
		}
	}
	for _, expr := range []string{"id > 0 /*+ optimizer hint */", "name = '/*! literal */'", "id > 0 /* normal /*! comment */"} {
		if _, _, err := builder.Select("id").From("items").Where(expr).Build(); err != nil {
			t.Fatalf("non-executable token rejected: %s: %v", expr, err)
		}
	}
}
