package sqlbuilder

import (
	"strings"
	"testing"

	"gochen/errors"
)

func TestConditionsRejectStatementTerminators(t *testing.T) {
	for _, dialectName := range []string{"sqlite", "postgres", "mysql"} {
		for _, expr := range []string{
			"id > 0;",
			"id > 0; SELECT 1",
			"id > 0\x00",
			"id > 0 -- \x00 comment\n",
			"id > 0 /* \x00 comment */",
			"'\x00' = ''",
		} {
			for _, operation := range []string{"select", "or", "update", "delete"} {
				t.Run(dialectName+"/"+operation+"/"+expr, func(t *testing.T) {
					builder, err := New(&fakeDB{dialectName: dialectName})
					if err != nil {
						t.Fatal(err)
					}
					var query string
					var args []any
					switch operation {
					case "select":
						query, args, err = builder.Select("id").From("items").Where(expr).Where("tenant_id = 1").Build()
					case "or":
						query, args, err = builder.Select("id").From("items").Where("id = 0").Or(expr).Where("tenant_id = 1").Build()
					case "update":
						query, args, err = builder.Update("items").Set("value", 1).Where(expr).Where("tenant_id = 1").Build()
					case "delete":
						query, args, err = builder.DeleteFrom("items").Where(expr).Where("tenant_id = 1").Build()
					}
					if errors.Is(err, errors.InvalidInput) {
						return
					}
					// 修复前让真实驱动执行构造结果，证明截断后的租户越界及
					// 分号后的语法错误不能撤销第一条语句已经发生的副作用。
					if err == nil && dialectName == "sqlite" && operation != "or" {
						database := openCommentTestDB(t)
						if operation == "select" {
							rows, queryErr := database.Query(query, args...)
							count := 0
							if queryErr == nil {
								for rows.Next() {
									count++
								}
								queryErr = rows.Err()
								_ = rows.Close()
							}
							t.Fatalf("unsafe condition accepted: rows=%d driver error=%v query=%q", count, queryErr, query)
						}
						_, execErr := database.Exec(query, args...)
						var count int
						if err := database.QueryRow("SELECT COUNT(*) FROM items WHERE tenant_id = 2 AND value = 0").Scan(&count); err != nil {
							t.Fatal(err)
						}
						t.Fatalf("unsafe condition accepted: other tenant unchanged rows=%d (want 1), driver error=%v query=%q", count, execErr, query)
					}
					t.Fatalf("expected InvalidInput, got %v for %q", err, query)
				})
			}
		}
	}
}

func TestConditionsAllowSemicolonsInTokensAndBoundData(t *testing.T) {
	for _, dialectName := range []string{"sqlite", "postgres", "mysql"} {
		expressions := []string{"';' = ';'", "id > 0 /* ; */", "id > 0 -- ;", `"semi;colon" = "semi;colon"`}
		switch dialectName {
		case "sqlite":
			expressions = append(expressions, "[semi;colon] = [semi;colon]", "`semi;colon` = `semi;colon`")
		case "postgres":
			expressions = append(expressions, "$$;$$ = $tag$;$tag$", `E'\';' = E'\';'`)
		case "mysql":
			expressions = append(expressions, "id > 0 # ;", `name = '\';'`)
		}
		for _, expr := range expressions {
			t.Run(dialectName+"/"+expr, func(t *testing.T) {
				builder, err := New(&fakeDB{dialectName: dialectName})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := builder.Select("id").From("items").Where(expr).Where("tenant_id = 1").Build(); err != nil {
					t.Fatalf("quoted/comment semicolon rejected: %v", err)
				}
			})
		}
	}
	// 绑定参数中的 NUL 与分号是数据，必须完整保留。
	builder, err := New(&fakeDB{dialectName: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	value := "data;\x00tail"
	query, args, err := builder.Select("id").From("items").Where("? = ?", value, value).Where("tenant_id = 1").Build()
	if err != nil || strings.ContainsRune(query, 0) || len(args) != 2 || args[0] != value || args[1] != value {
		t.Fatalf("bound data modified: query=%q args=%q err=%v", query, args, err)
	}
	database := openCommentTestDB(t)
	var id int
	if err := database.QueryRow(query, args...).Scan(&id); err != nil || id != 1 {
		t.Fatalf("bound data query: id=%d err=%v", id, err)
	}
}
