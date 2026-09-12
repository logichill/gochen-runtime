package contracttest

import (
	"context"
	"fmt"
	"testing"

	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/errors"
)

type queryRecord struct {
	ID    int64   `db:"id" gorm:"column:id;primaryKey"`
	Order string  `db:"order" gorm:"column:order"`
	Kind  string  `db:"kind" gorm:"column:kind"`
	Note  *string `db:"note" gorm:"column:note"`
}

func runQuery(t *testing.T, factory func(*testing.T) orm.IOrm) {
	t.Helper()
	adapter := factory(t)
	database := adapter.Database()
	d := dialect.FromDatabase(database)
	ctx := context.Background()
	for _, query := range []string{
		fmt.Sprintf(`CREATE TABLE query_items (id BIGINT PRIMARY KEY, %s VARCHAR(255), kind VARCHAR(255), note VARCHAR(255))`, d.QuoteIdentifier("order")),
		fmt.Sprintf(`CREATE TABLE %s (id BIGINT PRIMARY KEY, kind VARCHAR(255))`, d.QuoteIdentifier("group")),
		`INSERT INTO query_items VALUES (1, 'first', 'same', NULL), (2, 'second', 'same', 'b'), (3, 'third', 'other', 'c')`,
		fmt.Sprintf(`INSERT INTO %s VALUES (1, 'same'), (3, 'other')`, d.QuoteIdentifier("group")),
	} {
		if _, err := database.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	model, err := adapter.Model(&orm.ModelMeta{Table: "query_items", ModelFactory: orm.NewModelFactory[*queryRecord]()})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ReservedSelect", func(t *testing.T) {
		for _, column := range []string{"order", "query_items.order"} {
			for _, raw := range []bool{false, true} {
				opts := []orm.QueryOption{orm.WithSelect(column), orm.WithOrderBy("id", false)}
				if raw {
					opts = append(opts, orm.WithSelectExprUnsafe("1 AS marker"))
				}
				var rows []queryRecord
				if err := model.Find(ctx, &rows, opts...); err != nil || len(rows) != 3 || rows[0].Order != "first" {
					t.Errorf("column=%q raw=%v: rows=%+v err=%v", column, raw, rows, err)
				}
			}
		}
	})

	t.Run("ReservedJoin", func(t *testing.T) {
		for _, alias := range []string{"", "select"} {
			target := alias
			if target == "" {
				target = "group"
			}
			on := []orm.JoinOn{orm.On("query_items.id", target+".id"), orm.On("query_items.kind", target+".kind")}
			for _, conditions := range [][]orm.JoinOn{on[:1], on} {
				var rows []queryRecord
				err := model.Find(ctx, &rows, orm.WithSelect("query_items.id"), orm.WithJoin(orm.InnerJoin("group", alias, conditions...)))
				if err != nil || len(rows) != 2 {
					t.Errorf("alias=%q conditions=%v: rows=%+v err=%v", alias, conditions, rows, err)
				}
			}
		}
		var rows []queryRecord
		err := model.Find(ctx, &rows, orm.WithJoin(orm.InnerJoin("group", "", orm.On("query_items.id", "group.id"), orm.On("query_items.kind", "group.kind OR 1=1"))))
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("unsafe subsequent ON accepted: %v", err)
		}
	})

	t.Run("Count", func(t *testing.T) {
		join := orm.WithJoin(orm.InnerJoin("group", "g", orm.On("query_items.id", "g.id"), orm.On("query_items.kind", "g.kind")))
		for _, tc := range []struct {
			name string
			opts []orm.QueryOption
			want int64
		}{
			{"all", nil, 3},
			{"empty", []orm.QueryOption{orm.WithWhere("id > ?", 99)}, 0},
			{"pagination", []orm.QueryOption{orm.WithLimit(1), orm.WithOffset(1)}, 3},
			{"nullable projection", []orm.QueryOption{orm.WithSelect("note")}, 3},
			{"mixed projection", []orm.QueryOption{orm.WithSelect("note"), orm.WithSelectExprUnsafe("1 AS marker")}, 3},
			{"groups", []orm.QueryOption{orm.WithGroupBy("kind")}, 2},
			{"group pagination", []orm.QueryOption{orm.WithGroupBy("kind"), orm.WithLimit(1), orm.WithOffset(1)}, 2},
			{"empty groups", []orm.QueryOption{orm.WithGroupBy("kind"), orm.WithWhere("id > ?", 99)}, 0},
			{"filtered group", []orm.QueryOption{orm.WithGroupBy("kind"), orm.WithWhere("kind = ?", "same")}, 1},
			{"join", []orm.QueryOption{join}, 2},
			{"filtered join", []orm.QueryOption{join, orm.WithWhere("query_items.id < ?", 3)}, 1},
			{"grouped join", []orm.QueryOption{join, orm.WithGroupBy("query_items.kind"), orm.WithLimit(1)}, 2},
		} {
			t.Run(tc.name, func(t *testing.T) {
				count, err := model.Count(ctx, tc.opts...)
				if err != nil || count != tc.want {
					t.Fatalf("Count = %d, %v; want %d", count, err, tc.want)
				}
			})
		}
	})
}
