package sqlbuilder

import (
	"gochen/errors"
	"testing"
)

func TestConditionsRejectUnbalancedParentheses(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			sqlb, err := New(&fakeDB{dialectName: dialect})
			if err != nil {
				t.Fatal(err)
			}
			for _, expr := range []string{"(id = 1", "id = 1)", ") OR (id = 1", "((id = 1)"} {
				builds := []func() (string, []any, error){
					sqlb.Select("id").From("items").Where(expr).Build,
					sqlb.Update("items").Set("value", 1).Where(expr).Build,
					sqlb.DeleteFrom("items").Where(expr).Build,
				}
				for _, build := range builds {
					if _, _, err := build(); !errors.Is(err, errors.InvalidInput) {
						t.Fatalf("%q: %v", expr, err)
					}
				}
			}
			if _, _, err := sqlb.Select("id").From("items").Where("(name = ')' OR name = '(') /* ) */ AND id = ?", 1).Build(); err != nil {
				t.Fatalf("quoted/comment parentheses rejected: %v", err)
			}
		})
	}
}
