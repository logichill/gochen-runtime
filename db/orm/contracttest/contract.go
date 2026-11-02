// Package contracttest 为 ORM 适配器提供共同的字段安全、错误语义与事务行为测试。
package contracttest

import (
	"context"
	"testing"
	"time"

	"gochen-runtime/db/orm/repo"
	"gochen/db/orm"
	"gochen/domain/spec"
	"gochen/errors"
)

type record struct {
	ID      int64  `db:"id" gorm:"column:id;primaryKey" entity:"id"`
	Name    string `db:"name" gorm:"column:name"`
	Version uint64 `db:"version" gorm:"column:version"`
}

func (r *record) GetID() int64        { return r.ID }
func (r *record) GetVersion() uint64  { return r.Version }
func (r *record) SetVersion(v uint64) { r.Version = v }

type panicRecord record

func (r *panicRecord) GetID() int64        { panic("entity GetID panic") }
func (r *panicRecord) GetVersion() uint64  { return r.Version }
func (r *panicRecord) SetVersion(v uint64) { r.Version = v }
func (r *panicRecord) SetID(id int64)      { r.ID = id }

type updatePanicRecord record

func (r *updatePanicRecord) GetID() int64 {
	if r.ID == 3 {
		panic("update GetID panic")
	}
	return r.ID
}

func (r *updatePanicRecord) GetVersion() uint64  { return r.Version }
func (r *updatePanicRecord) SetVersion(v uint64) { r.Version = v }

// Run 使用独立数据库工厂验证适配器；工厂负责通过 t.Cleanup 关闭数据库。
// 返回的 ORM 必须暴露 Database，数据库须支持标准建表语句和事务。
func Run(t *testing.T, factory func(*testing.T) orm.IOrm) {
	t.Helper()
	t.Run("Query", func(t *testing.T) { runQuery(t, factory) })
	fixture := func(t *testing.T) (orm.IOrm, orm.IModel) {
		t.Helper()
		adapter := factory(t)
		if adapter.Database() == nil {
			t.Fatal("factory must expose a database")
		}
		_, err := adapter.Database().Exec(context.Background(), `CREATE TABLE contract_records (
			id BIGINT PRIMARY KEY, name VARCHAR(255) NOT NULL UNIQUE CHECK (name <> 'invalid'), version BIGINT NOT NULL)`)
		if err != nil {
			t.Fatal(err)
		}
		model, err := adapter.Model(&orm.ModelMeta{Table: "contract_records", ModelFactory: orm.NewModelFactory[*record]()})
		if err != nil {
			t.Fatal(err)
		}
		if err := model.Create(context.Background(), &record{ID: 1, Name: "first", Version: 1}); err != nil {
			t.Fatal(err)
		}
		return adapter, model
	}

	t.Run("SelectFields", func(t *testing.T) {
		_, model := fixture(t)
		for _, column := range []string{"id, (SELECT 'unexpected') AS name", "name AS alias", "COUNT(*)"} {
			for _, raw := range []bool{false, true} {
				opts := []orm.QueryOption{orm.WithSelect(column)}
				if raw {
					opts = append(opts, orm.WithSelectExprUnsafe("1 AS marker"))
				}
				var rows []record
				if err := model.Find(context.Background(), &rows, opts...); !errors.Is(err, errors.InvalidInput) {
					t.Errorf("WithSelect(%q), raw=%v: %v, want InvalidInput", column, raw, err)
				}
			}
		}
		var rows []record
		if err := model.Find(context.Background(), &rows, orm.WithSelect("id", "name")); err != nil || len(rows) != 1 || rows[0].Name != "first" {
			t.Fatalf("valid field selection: rows=%+v err=%v", rows, err)
		}
		rows = nil
		if err := model.Find(context.Background(), &rows, orm.WithSelectExprUnsafe("id, 'expression' AS name")); err != nil || len(rows) != 1 || rows[0].Name != "expression" {
			t.Fatalf("explicit expression selection: rows=%+v err=%v", rows, err)
		}
	})

	t.Run("CreateConflict", func(t *testing.T) {
		adapter, _ := fixture(t)
		repository, err := repo.NewRepo[*record, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []*record{{ID: 1, Name: "another", Version: 1}, {ID: 2, Name: "first", Version: 1}} {
			if err := repository.Create(context.Background(), row, spec.All()); !errors.Is(err, errors.Conflict) || errors.Code(err) != errors.Conflict {
				t.Errorf("duplicate create = %v, want Conflict as the outer error code", err)
			}
		}
		if err := repository.Create(context.Background(), &record{ID: 2, Name: "invalid", Version: 1}, spec.All()); errors.Code(err) != errors.Database {
			t.Fatalf("CHECK violation = %v, want Database", err)
		}
	})

	t.Run("BulkConflictRollback", func(t *testing.T) {
		adapter, model := fixture(t)
		repository, err := repo.NewRepo[*record, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		err = repository.CreateBulk(context.Background(), []*record{{ID: 2, Name: "second", Version: 1}, {ID: 3, Name: "first", Version: 1}}, spec.All())
		if errors.Code(err) != errors.Conflict {
			t.Fatalf("bulk create = %v, want Conflict", err)
		}
		count, err := model.Count(context.Background())
		if err != nil || count != 1 {
			t.Fatalf("partial bulk survived: count=%d err=%v", count, err)
		}
	})
	t.Run("UpdateConflict", func(t *testing.T) {
		adapter, model := fixture(t)
		if err := model.Create(context.Background(), &record{ID: 2, Name: "second", Version: 1}); err != nil {
			t.Fatal(err)
		}
		repository, err := repo.NewRepo[*record, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.Update(context.Background(), &record{ID: 2, Name: "first", Version: 1}, spec.All()); errors.Code(err) != errors.Conflict {
			t.Fatalf("unique update conflict = %v, want Conflict", err)
		}
	})
	t.Run("BulkUpdateConflictRollback", func(t *testing.T) {
		adapter, model := fixture(t)
		if err := model.Create(context.Background(), &record{ID: 2, Name: "second", Version: 1}); err != nil {
			t.Fatal(err)
		}
		repository, err := repo.NewRepo[*record, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		err = repository.UpdateBulk(context.Background(), []*record{
			{ID: 1, Name: "updated", Version: 1},
			{ID: 2, Name: "updated", Version: 1},
		}, spec.All())
		if errors.Code(err) != errors.Conflict {
			t.Fatalf("bulk update = %v, want Conflict", err)
		}
		var row record
		if err := model.First(context.Background(), &row, orm.WithWhere("id = ?", 1)); err != nil {
			t.Fatal(err)
		}
		if row.Name != "first" || row.Version != 1 {
			t.Fatalf("partial bulk update survived: %+v", row)
		}
	})
	t.Run("ModelBulkRollback", func(t *testing.T) {
		_, model := fixture(t)
		err := model.Create(context.Background(), &record{ID: 2, Name: "second", Version: 1}, &record{ID: 3, Name: "first", Version: 1})
		if err == nil {
			t.Fatal("expected duplicate constraint error")
		}
		count, err := model.Count(context.Background())
		if err != nil || count != 1 {
			t.Fatalf("partial model bulk survived: count=%d err=%v", count, err)
		}
	})
	t.Run("BulkPanicReleasesTransaction", func(t *testing.T) {
		adapter, model := fixture(t)
		repository, err := repo.NewRepo[*panicRecord, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		func() {
			defer func() {
				if got := recover(); got != "entity GetID panic" {
					t.Fatalf("panic=%v, want entity GetID panic", got)
				}
			}()
			_ = repository.CreateBulk(ctx, []*panicRecord{{ID: 2, Name: "second", Version: 1}}, spec.All())
		}()
		queryCtx, cancelQuery := context.WithTimeout(context.Background(), time.Second)
		defer cancelQuery()
		count, err := model.Count(queryCtx)
		if err != nil || count != 1 {
			t.Fatalf("transaction not released after panic: count=%d err=%v", count, err)
		}
	})
	t.Run("BulkUpdatePanicRollsBackWrittenRecords", func(t *testing.T) {
		adapter, model := fixture(t)
		repository, err := repo.NewRepo[*updatePanicRecord, int64](adapter, "contract_records")
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if got := recover(); got != "update GetID panic" {
					t.Fatalf("panic = %v, want update GetID panic", got)
				}
			}()
			_ = repository.UpdateBulk(context.Background(), []*updatePanicRecord{
				{ID: 1, Name: "updated", Version: 1},
				{ID: 3, Name: "panics", Version: 1},
			}, spec.All())
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var row record
		if err := model.First(ctx, &row, orm.WithWhere("id = ?", 1)); err != nil {
			t.Fatal(err)
		}
		if row.Name != "first" || row.Version != 1 {
			t.Fatalf("partial update survived panic: %+v", row)
		}
	})
}
