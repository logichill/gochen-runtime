package lite_test

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/contracttest"
	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/orm"

	_ "modernc.org/sqlite"
)

func TestORMContract(t *testing.T) {
	contracttest.Run(t, func(t *testing.T) orm.IOrm {
		database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := database.Close(); err != nil {
				t.Error(err)
			}
		})
		adapter, err := lite.New(database)
		if err != nil {
			t.Fatal(err)
		}
		return adapter
	})
}
