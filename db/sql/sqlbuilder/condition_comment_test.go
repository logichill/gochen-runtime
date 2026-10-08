package sqlbuilder

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTrailingLineCommentPreservesFollowingConditions(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		comments := []string{"-- visibility rule"}
		if dialect == "mysql" {
			comments = append(comments, "# visibility rule")
		}
		for _, comment := range comments {
			for _, operation := range []string{"select", "select_or_left", "select_or_right", "update", "delete"} {
				t.Run(dialect+"/"+comment+"/"+operation, func(t *testing.T) {
					builder, err := New(&fakeDB{dialectName: dialect})
					if err != nil {
						t.Fatal(err)
					}
					var query string
					var args []any
					condition := "id > 0 " + comment
					switch operation {
					case "select":
						query, args, err = builder.Select("id").From("items").Where(condition).Where("tenant_id = 1").Build()
					case "select_or_left":
						query, args, err = builder.Select("id").From("items").Where(condition).Or("id = 0").Where("tenant_id = 1").Build()
					case "select_or_right":
						query, args, err = builder.Select("id").From("items").Where("id = 0").Or(condition).Where("tenant_id = 1").Build()
					case "update":
						query, args, err = builder.Update("items").Set("value", 1).Where(condition).Where("tenant_id = 1").Build()
					case "delete":
						query, args, err = builder.DeleteFrom("items").Where(condition).Where("tenant_id = 1").Build()
					}
					if err != nil {
						t.Fatal(err)
					}
					// SQLite 能执行三种方言的此处标识符及参数；# 注释先转换为同义 --。
					if comment[0] == '#' {
						query = strings.Replace(query, "# visibility rule", "-- visibility rule", 1)
					}
					database, err := sql.Open("sqlite", ":memory:")
					if err != nil {
						t.Fatal(err)
					}
					defer database.Close()
					if _, err := database.Exec("CREATE TABLE items (id INTEGER, tenant_id INTEGER, value INTEGER); INSERT INTO items VALUES (1,1,0),(2,2,0)"); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(operation, "select") {
						rows, err := database.Query(query, args...)
						if err != nil {
							t.Fatal(err)
						}
						defer rows.Close()
						count := 0
						for rows.Next() {
							count++
						}
						if err := rows.Err(); err != nil {
							t.Fatal(err)
						}
						if count != 1 {
							t.Fatalf("query returned %d tenants' rows: %s", count, query)
						}
						return
					}
					result, err := database.Exec(query, args...)
					if err != nil {
						t.Fatal(err)
					}
					count, err := result.RowsAffected()
					if err != nil || count != 1 {
						t.Fatalf("statement affected %d rows, error %v: %s", count, err, query)
					}
				})
			}
		}
	}
}
